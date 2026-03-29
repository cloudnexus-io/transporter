package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/namespaces"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	pb "transporter/pkg/agent/api"
)

type agentServer struct {
	pb.UnimplementedMigrationServer
	currentPodName string
}

const (
	containerdAddr = "/run/containerd/containerd.sock"
	hostStore      = "/tmp/transporter/store"
)

func (s *agentServer) Prepare(ctx context.Context, in *pb.PrepareRequest) (*pb.PrepareResponse, error) {
	fmt.Printf("Preparing: %s\n", in.PodName)
	s.currentPodName = in.PodName
	podStore := filepath.Join("/host", hostStore, in.PodName)
	os.RemoveAll(podStore)
	os.MkdirAll(podStore, 0777)
	return &pb.PrepareResponse{Success: true, Message: "Ready"}, nil
}

func getContainerPaths(podName, podNamespace, containerID string) (upperDir string, rootfs string, pid int, foundID string, err error) {
	client, err := containerd.New(containerdAddr)
	if err != nil {
		return "", "", 0, "", err
	}
	defer client.Close()
	ctx := namespaces.WithNamespace(context.Background(), "k8s.io")

	containers, err := client.Containers(ctx)
	if err != nil {
		return "", "", 0, "", err
	}

	var targetContainer containerd.Container
	if containerID != "" {
		cleanID := strings.TrimPrefix(containerID, "containerd://")
		for _, c := range containers {
			if c.ID() == cleanID || strings.HasPrefix(c.ID(), cleanID) {
				targetContainer = c
				break
			}
		}
	}

	if targetContainer == nil {
		var latestTime time.Time
		for _, c := range containers {
			labels, _ := c.Labels(ctx)
			if labels["io.kubernetes.pod.name"] == podName && labels["io.kubernetes.pod.namespace"] == podNamespace {
				if labels["io.kubernetes.container.name"] == "POD" || labels["io.kubernetes.container.name"] == "pause" {
					continue
				}
				info, _ := c.Info(ctx)
				if info.CreatedAt.After(latestTime) {
					latestTime = info.CreatedAt
					targetContainer = c
				}
			}
		}
	}

	if targetContainer == nil {
		return "", "", 0, "", fmt.Errorf("container not found")
	}

	task, err := targetContainer.Task(ctx, nil)
	if err != nil {
		return "", "", 0, "", err
	}
	pid = int(task.Pid())
	foundID = targetContainer.ID()

	cmd := exec.Command("nsenter", "-t", "1", "-m", "mount")
	out, _ := cmd.Output()
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		if strings.Contains(line, foundID) && strings.Contains(line, "type overlay") {
			// Find upperdir=
			idx := strings.Index(line, "upperdir=")
			if idx != -1 {
				sub := line[idx+len("upperdir="):]
				endIdx := strings.IndexAny(sub, ",)")
				if endIdx != -1 {
					upperDir = sub[:endIdx]
				} else {
					upperDir = sub
				}
			}
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				rootfs = fields[2]
			}
			if upperDir != "" && rootfs != "" {
				return upperDir, rootfs, pid, foundID, nil
			}
		}
	}
	return "", "", 0, "", fmt.Errorf("overlay paths not found")
}

func (s *agentServer) StartMigration(ctx context.Context, in *pb.StartMigrationRequest) (*pb.StartMigrationResponse, error) {
	fmt.Printf("CAPTURE START: %s\n", in.PodName)
	conn, err := grpc.Dial(in.TargetAddress, grpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	client := pb.NewMigrationClient(conn)
	fsStream, err := client.TransferFilesystem(ctx)
	if err != nil {
		return nil, err
	}

	upperDir, _, pid, containerID, err := getContainerPaths(in.PodName, in.PodNamespace, in.ContainerId)
	podStoreInAgent := filepath.Join("/host", hostStore, in.PodName)

	if err == nil {
		fmt.Printf("CAPTURING Layers [%s]: %s\n", containerID, upperDir)
		os.MkdirAll(podStoreInAgent, 0777)

		checkpointDirInAgent := filepath.Join(podStoreInAgent, "checkpoint")
		os.MkdirAll(checkpointDirInAgent, 0777)
		fmt.Printf("CRIU DUMP PID %d\n", pid)

		args := []string{"dump", "-t", fmt.Sprintf("%d", pid), "-D", checkpointDirInAgent,
			"-j", "--tcp-established", "--file-locks", "--shell-job", "--link-remap", "--manage-cgroups=none",
			"--skip-mnt", "/etc/resolv.conf", "--skip-mnt", "/etc/hostname", "--skip-mnt", "/etc/hosts",
			"--skip-mnt", "/dev/termination-log", "--ext-mount-map", "/etc/resolv.conf:/etc/resolv.conf"}

		criuCmd := exec.Command("criu", args...)
		if out, err := criuCmd.CombinedOutput(); err != nil {
			fmt.Printf("CRIU ERROR: %v\nOutput: %s\n", err, string(out))
		} else {
			fmt.Println("CRIU SUCCESS")
		}

		// NOW capture FS while process is paused
		tarPath := filepath.Join(podStoreInAgent, "layer.tar")
		hostUpper := filepath.Join("/host", upperDir)
		fmt.Printf("CAPTURING FS: %s -> %s\n", hostUpper, tarPath)
		if out, err := exec.Command("tar", "-C", hostUpper, "-cf", tarPath, ".").CombinedOutput(); err != nil {
			fmt.Printf("TAR ERROR: %v (%s)\n", err, string(out))
		}
	}

	// CHUNKED STREAMING to avoid memory exhaustion
	count := 0
	filepath.Walk(podStoreInAgent, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		relPath, _ := filepath.Rel(podStoreInAgent, path)

		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()

		buffer := make([]byte, 1024*1024) // 1MB chunks
		for {
			n, err := file.Read(buffer)
			if n > 0 {
				fsStream.Send(&pb.FileChunk{Path: relPath, Data: buffer[:n]})
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
		}
		count++
		return nil
	})

	fmt.Printf("STREAMED %d files total.\n", count)
	fsStream.CloseAndRecv()
	return &pb.StartMigrationResponse{Success: true, Message: "Done"}, nil
}

func (s *agentServer) TransferFilesystem(stream pb.Migration_TransferFilesystemServer) error {
	podName := s.currentPodName
	agentStorePath := filepath.Join("/host", hostStore, podName)
	os.MkdirAll(agentStorePath, 0777)
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		targetPath := filepath.Join(agentStorePath, chunk.Path)
		os.MkdirAll(filepath.Dir(targetPath), 0777)

		f, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			return err
		}
		f.Write(chunk.Data)
		f.Close()
	}
	return stream.SendAndClose(&pb.TransferResponse{Success: true})
}

func getProcessCgroup(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/host/proc/%d/cgroup", pid))
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.Contains(line, "::/") {
			parts := strings.Split(line, "::")
			if len(parts) > 1 {
				return parts[1], nil
			}
		}
	}
	return "", fmt.Errorf("cgroup not found")
}

func (s *agentServer) ApplyLayer(ctx context.Context, in *pb.ApplyLayerRequest) (*pb.ApplyLayerResponse, error) {
	fmt.Printf("INJECT START: %s\n", in.PodName)

	var rootfs string
	var pid int
	var err error
	for i := 0; i < 60; i++ {
		_, rootfs, pid, _, err = getContainerPaths(in.PodName, in.PodNamespace, in.ContainerId)
		if err == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}
	if err != nil {
		return nil, err
	}

	podStoreInAgent := filepath.Join("/host", hostStore, in.PodName)
	tarPath := filepath.Join(podStoreInAgent, "layer.tar")

	fmt.Printf("INJECTING Layers [%s] into PID %d mount ns\n", tarPath, pid)
	if _, err := os.Stat(tarPath); err == nil {
		f, err := os.Open(tarPath)
		if err == nil {
			// Extract directly into the container's mount namespace, ignoring read-only mounts
			cmd := exec.Command("nsenter", "-t", fmt.Sprintf("%d", pid), "-m", "tar", "-C", "/", "-xpf", "-", "--exclude=./run/secrets/*")
			cmd.Stdin = f
			out, err := cmd.CombinedOutput()
			f.Close()
			if err != nil && !strings.Contains(string(out), "Read-only file system") {
				fmt.Printf("INJECT ERROR: %v (%s)\n", err, string(out))
			} else {
				fmt.Println("INJECT SUCCESS (some non-critical errors might be ignored)")
			}
		}
		// Refresh caches
		exec.Command("nsenter", "-t", "1", "-m", "sh", "-c", "echo 3 > /proc/sys/vm/drop_caches").Run()
	}

	checkpointDirInAgent := filepath.Join(podStoreInAgent, "checkpoint")
	if _, err := os.Stat(checkpointDirInAgent); err == nil {
		targetCgroup, _ := getProcessCgroup(pid)
		fmt.Printf("CRIU RESTORE PID %d (Target CG: %s)\n", pid, targetCgroup)
		hostRootfs := filepath.Join("/host", rootfs)

		// Map cgroups if possible. CRIU 3.16+ supports --cgroup-replace
		// We'll try to use a more generic approach: --manage-cgroups=none and --cgroup-root
		// Use more aggressive cgroup settings for K8s
		restoreCmd := fmt.Sprintf("mount -t proc none /proc && criu restore -D %s --root %s -j --tcp-established --file-locks --shell-job --link-remap --manage-cgroups=none --skip-mnt /etc/resolv.conf --skip-mnt /etc/hostname --skip-mnt /etc/hosts --ext-mount-map /etc/resolv.conf:/etc/resolv.conf --cgroup-root /", checkpointDirInAgent, hostRootfs)

		nsArgs := []string{"-t", fmt.Sprintf("%d", pid), "-u", "-i", "-n", "-p", "sh", "-c", restoreCmd}
		if out, err := exec.Command("nsenter", nsArgs...).CombinedOutput(); err != nil {
			fmt.Printf("CRIU ERROR: %v\nOutput: %s\n", err, string(out))
			return &pb.ApplyLayerResponse{Success: false, Message: fmt.Sprintf("CRIU Restore failed: %v", err)}, nil
		} else {
			fmt.Println("CRIU SUCCESS")
		}
	}
	return &pb.ApplyLayerResponse{Success: true, Message: "Applied"}, nil
}

func (s *agentServer) SignalHandover(ctx context.Context, in *pb.SignalHandoverRequest) (*pb.SignalHandoverResponse, error) {
	fmt.Printf("SIGNAL HANDOVER: %s\n", in.PodName)

	httpClient := &http.Client{Timeout: 5 * time.Second}

	sidecarPodName := in.PodName + "-ghost"
	resp, err := httpClient.Get(fmt.Sprintf("http://%s:50053/handover", sidecarPodName))
	if err != nil {
		fmt.Printf("Handover signal failed: %v\n", err)
		return &pb.SignalHandoverResponse{Success: false, Message: err.Error()}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		fmt.Println("Handover successful")
		return &pb.SignalHandoverResponse{Success: true, Message: "Handover completed"}, nil
	}

	return &pb.SignalHandoverResponse{Success: false, Message: "Handover failed"}, nil
}

func main() {
	os.MkdirAll("/host/tmp/transporter/store", 0777)
	lis, _ := net.Listen("tcp", ":50051")
	grpcServer := grpc.NewServer()
	pb.RegisterMigrationServer(grpcServer, &agentServer{})
	reflection.Register(grpcServer)
	fmt.Printf("Ghost-Sync Agent (V10 Stable) listening on %v\n", lis.Addr())
	grpcServer.Serve(lis)
}
