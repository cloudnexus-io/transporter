package main

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	migrationv1alpha1 "transporter/api/v1alpha1"
	pb "transporter/pkg/agent/api"
)

type PodMigrationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const agentPort = 50051

func getNodeInternalIP(node *corev1.Node) (string, error) {
	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			return addr.Address, nil
		}
	}
	return "", fmt.Errorf("no IP")
}

func (r *PodMigrationReconciler) callAgentPrepare(ctx context.Context, nodeIP string, podName, podNamespace string) error {
	conn, _ := grpc.Dial(fmt.Sprintf("%s:%d", nodeIP, agentPort), grpc.WithInsecure())
	defer conn.Close()
	client := pb.NewMigrationClient(conn)
	client.Prepare(ctx, &pb.PrepareRequest{PodName: podName, PodNamespace: podNamespace})
	return nil
}

func (r *PodMigrationReconciler) callAgentStart(ctx context.Context, sourceIP, targetIP string, podName, podNamespace string, containerID string) error {
	conn, _ := grpc.Dial(fmt.Sprintf("%s:%d", sourceIP, agentPort), grpc.WithInsecure())
	defer conn.Close()
	client := pb.NewMigrationClient(conn)
	resp, err := client.StartMigration(ctx, &pb.StartMigrationRequest{
		PodName: podName, PodNamespace: podNamespace, TargetAddress: fmt.Sprintf("%s:%d", targetIP, agentPort), ContainerId: containerID,
	})
	if err != nil || !resp.Success {
		return fmt.Errorf("failed")
	}
	return nil
}

func (r *PodMigrationReconciler) callAgentApply(ctx context.Context, nodeIP string, podName, podNamespace string, containerID string) error {
	conn, _ := grpc.Dial(fmt.Sprintf("%s:%d", nodeIP, agentPort), grpc.WithInsecure())
	defer conn.Close()
	client := pb.NewMigrationClient(conn)
	resp, err := client.ApplyLayer(ctx, &pb.ApplyLayerRequest{PodName: podName, PodNamespace: podNamespace, ContainerId: containerID})
	if err != nil || !resp.Success {
		return fmt.Errorf("failed")
	}
	return nil
}

func (r *PodMigrationReconciler) callTransferFilesystemToNode(ctx context.Context, sourceIP, targetIP string, podName string) error {
	conn, _ := grpc.Dial(fmt.Sprintf("%s:%d", sourceIP, agentPort), grpc.WithInsecure())
	defer conn.Close()
	client := pb.NewMigrationClient(conn)
	resp, err := client.TransferFilesystemToNode(ctx, &pb.TransferToNodeRequest{
		PodName:       podName,
		TargetAddress: fmt.Sprintf("%s:%d", targetIP, agentPort),
	})
	if err != nil || !resp.Success {
		return fmt.Errorf("filesystem transfer failed: %v", resp.GetMessage())
	}
	return nil
}

func (r *PodMigrationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	mig := &migrationv1alpha1.PodMigration{}
	if err := r.Get(ctx, req.NamespacedName, mig); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	switch mig.Status.Phase {
	case "":
		mig.Status.Phase = migrationv1alpha1.PodMigrationPhasePending
		r.Update(ctx, mig)
		return ctrl.Result{Requeue: true}, nil

	case migrationv1alpha1.PodMigrationPhasePending:
		pod := &corev1.Pod{}
		if err := r.Get(ctx, types.NamespacedName{Name: mig.Spec.PodName, Namespace: mig.Spec.Namespace}, pod); err != nil {
			return ctrl.Result{}, err
		}
		mig.Spec.SourceNode = pod.Spec.NodeName
		mig.Status.Phase = migrationv1alpha1.PodMigrationPhaseSyncing
		mig.Status.Message = "Creating Ghost Pod"
		r.Update(ctx, mig)
		return ctrl.Result{Requeue: true}, nil

	case migrationv1alpha1.PodMigrationPhaseSyncing:
		ghostName := mig.Spec.PodName + "-ghost"
		ghost := &corev1.Pod{}
		err := r.Get(ctx, types.NamespacedName{Name: ghostName, Namespace: mig.Spec.Namespace}, ghost)
		if errors.IsNotFound(err) {
			ghost = &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: ghostName, Namespace: mig.Spec.Namespace},
				Spec: corev1.PodSpec{
					NodeName:   mig.Spec.TargetNode,
					Containers: []corev1.Container{{Name: "ubuntu", Image: "ubuntu", Command: []string{"sleep", "infinity"}}},
				},
			}
			r.Create(ctx, ghost)
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		if ghost.Status.Phase != corev1.PodRunning || len(ghost.Status.ContainerStatuses) == 0 || ghost.Status.ContainerStatuses[0].ContainerID == "" {
			return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
		}

		targetNode := &corev1.Node{}
		r.Get(ctx, types.NamespacedName{Name: mig.Spec.TargetNode}, targetNode)
		targetIP, _ := getNodeInternalIP(targetNode)
		r.callAgentPrepare(ctx, targetIP, mig.Spec.PodName, mig.Spec.Namespace)

		sourcePod := &corev1.Pod{}
		r.Get(ctx, types.NamespacedName{Name: mig.Spec.PodName, Namespace: mig.Spec.Namespace}, sourcePod)
		sourceNode := &corev1.Node{}
		r.Get(ctx, types.NamespacedName{Name: mig.Spec.SourceNode}, sourceNode)
		sourceIP, _ := getNodeInternalIP(sourceNode)

		l.Info("GHOST-SYNC: Capture Source -> Store")
		if err := r.callAgentStart(ctx, sourceIP, targetIP, mig.Spec.PodName, mig.Spec.Namespace, sourcePod.Status.ContainerStatuses[0].ContainerID); err != nil {
			return ctrl.Result{}, err
		}

		l.Info("GHOST-SYNC: Transfer filesystem to target node")
		if err := r.callTransferFilesystemToNode(ctx, sourceIP, targetIP, mig.Spec.PodName); err != nil {
			l.Error(err, "Filesystem transfer failed", "source", sourceIP, "target", targetIP)
		}

		l.Info("GHOST-SYNC: Ghost Injection")
		r.callAgentApply(ctx, targetIP, mig.Spec.PodName, mig.Spec.Namespace, ghost.Status.ContainerStatuses[0].ContainerID)

		mig.Status.Phase = migrationv1alpha1.PodMigrationPhaseFinalizing
		mig.Status.Message = "Swapping to final pod name"
		r.Update(ctx, mig)
		return ctrl.Result{Requeue: true}, nil

	case migrationv1alpha1.PodMigrationPhaseFinalizing:
		// Check if they exist first
		source := &corev1.Pod{}
		errS := r.Get(ctx, types.NamespacedName{Name: mig.Spec.PodName, Namespace: mig.Spec.Namespace}, source)
		ghost := &corev1.Pod{}
		errG := r.Get(ctx, types.NamespacedName{Name: mig.Spec.PodName + "-ghost", Namespace: mig.Spec.Namespace}, ghost)

		if !errors.IsNotFound(errS) || !errors.IsNotFound(errG) {
			if errS == nil {
				l.Info("RESYNC: Performing Final Capture before deleting source")
				sourceNode := &corev1.Node{}
				r.Get(ctx, types.NamespacedName{Name: mig.Spec.SourceNode}, sourceNode)
				sourceIP, _ := getNodeInternalIP(sourceNode)
				targetNode := &corev1.Node{}
				r.Get(ctx, types.NamespacedName{Name: mig.Spec.TargetNode}, targetNode)
				targetIP, _ := getNodeInternalIP(targetNode)
				r.callAgentStart(ctx, sourceIP, targetIP, mig.Spec.PodName, mig.Spec.Namespace, source.Status.ContainerStatuses[0].ContainerID)
			}

			l.Info("Cleanup old pods")
			gp := int64(0)
			if errS == nil {
				r.Delete(ctx, source, &client.DeleteOptions{GracePeriodSeconds: &gp})
			}
			if errG == nil {
				r.Delete(ctx, ghost, &client.DeleteOptions{GracePeriodSeconds: &gp})
			}
			return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
		}

		l.Info("Creating final pod")
		finalPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: mig.Spec.PodName, Namespace: mig.Spec.Namespace},
			Spec: corev1.PodSpec{
				NodeName:   mig.Spec.TargetNode,
				Containers: []corev1.Container{{Name: "ubuntu", Image: "ubuntu", Command: []string{"sleep", "infinity"}}},
			},
		}
		if err := r.Create(ctx, finalPod); err != nil {
			return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
		}

		targetNode := &corev1.Node{}
		r.Get(ctx, types.NamespacedName{Name: mig.Spec.TargetNode}, targetNode)
		targetIP, _ := getNodeInternalIP(targetNode)

		for i := 0; i < 20; i++ {
			p := &corev1.Pod{}
			r.Get(ctx, types.NamespacedName{Name: mig.Spec.PodName, Namespace: mig.Spec.Namespace}, p)
			if len(p.Status.ContainerStatuses) > 0 && p.Status.ContainerStatuses[0].ContainerID != "" {
				l.Info("Final Injection into Permanent Pod")
				r.callAgentApply(ctx, targetIP, mig.Spec.PodName, mig.Spec.Namespace, p.Status.ContainerStatuses[0].ContainerID)
				break
			}
			time.Sleep(1 * time.Second)
		}

		mig.Status.Phase = migrationv1alpha1.PodMigrationPhaseCompleted
		mig.Status.Message = "Successful"
		r.Update(ctx, mig)
		return ctrl.Result{}, nil

	default:
		return ctrl.Result{}, nil
	}
}

func (r *PodMigrationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&migrationv1alpha1.PodMigration{}).Complete(r)
}
