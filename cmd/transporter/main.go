package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	kubeconfig string
	namespace  string
)

// GroupVersionResource for the PodMigration CRD.
var podMigrationGVR = schema.GroupVersionResource{
	Group:    "migration.transporter.io",
	Version:  "v1alpha1",
	Resource: "podmigrations",
}

func main() {
	rootCmd := &cobra.Command{
		Use:   "transporter",
		Short: "Kubernetes pod migration orchestrator",
	}

	rootCmd.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig (defaults to KUBECONFIG or ~/.kube/config)")
	rootCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "default", "Kubernetes namespace")

	rootCmd.AddCommand(newMigrateCmd())
	rootCmd.AddCommand(newStatusCmd())
	rootCmd.AddCommand(newListCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func buildConfig() (*rest.Config, error) {
	// Explicit flag wins.
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	// Then env / default kubeconfig path.
	if env := os.Getenv("KUBECONFIG"); env != "" {
		return clientcmd.BuildConfigFromFlags("", env)
	}
	return clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
}

func newMigrateCmd() *cobra.Command {
	var targetNode string
	var strategy string

	cmd := &cobra.Command{
		Use:   "migrate <pod-name> -t <target-node>",
		Short: "Start a pod migration by creating a PodMigration CR",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			podName := args[0]
			if targetNode == "" {
				return fmt.Errorf("target node (-t) is required")
			}

			cfg, err := buildConfig()
			if err != nil {
				return err
			}

			ctx := context.Background()

			// Verify the pod exists.
			clientset, err := kubernetes.NewForConfig(cfg)
			if err != nil {
				return err
			}
			if _, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{}); err != nil {
				return fmt.Errorf("failed to find pod %s/%s: %w", namespace, podName, err)
			}

			dyn, err := dynamic.NewForConfig(cfg)
			if err != nil {
				return err
			}

			obj := map[string]interface{}{
				"apiVersion": "migration.transporter.io/v1alpha1",
				"kind":       "PodMigration",
				"metadata": map[string]interface{}{
					"generateName": "mig-",
					"namespace":    namespace,
				},
				"spec": map[string]interface{}{
					"podName":    podName,
					"namespace":  namespace,
					"targetNode": targetNode,
					"strategy":   strategy,
				},
			}

			u := &unstructured.Unstructured{Object: obj}
			res, err := dyn.Resource(podMigrationGVR).Namespace(namespace).Create(ctx, u, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create PodMigration: %w", err)
			}

			fmt.Printf("Migration request created\nMigration ID: %s\n", res.GetName())
			return nil
		},
	}

	cmd.Flags().StringVarP(&targetNode, "target-node", "t", "", "Target node for migration")
	cmd.Flags().StringVar(&strategy, "strategy", "live", "Migration strategy: live or cold")

	return cmd
}

func newStatusCmd() *cobra.Command {
	var migNamespace string

	cmd := &cobra.Command{
		Use:   "status <migration-id>",
		Short: "Show status of a PodMigration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			migID := args[0]
			if migNamespace == "" {
				migNamespace = namespace
			}

			cfg, err := buildConfig()
			if err != nil {
				return err
			}

			dyn, err := dynamic.NewForConfig(cfg)
			if err != nil {
				return err
			}

			ctx := context.Background()
			res, err := dyn.Resource(podMigrationGVR).Namespace(migNamespace).Get(ctx, migID, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("failed to get PodMigration %s/%s: %w", migNamespace, migID, err)
			}

			status, _, _ := unstructured.NestedMap(res.Object, "status")
			phase, _, _ := unstructured.NestedString(status, "phase")
			message, _, _ := unstructured.NestedString(status, "message")

			fmt.Println("+-----------+--------------------------------------+")
			fmt.Println("|  PHASE    | MESSAGE                              |")
			fmt.Println("+-----------+--------------------------------------+")
			fmt.Printf("| %-9s | %-36s |\n", phase, message)
			fmt.Println("+-----------+--------------------------------------+")

			return nil
		},
	}

	cmd.Flags().StringVarP(&migNamespace, "namespace", "n", "", "Namespace of the PodMigration (defaults to global --namespace)")

	return cmd
}

func newListCmd() *cobra.Command {
	var listNamespace string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List PodMigration resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			if listNamespace == "" {
				listNamespace = namespace
			}

			cfg, err := buildConfig()
			if err != nil {
				return err
			}

			dyn, err := dynamic.NewForConfig(cfg)
			if err != nil {
				return err
			}

			ctx := context.Background()
			list, err := dyn.Resource(podMigrationGVR).Namespace(listNamespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				return fmt.Errorf("failed to list PodMigrations in %s: %w", listNamespace, err)
			}

			fmt.Println("+----------------------+------------+-----------+")
			fmt.Println("| MIGRATION ID         | NAMESPACE  | PHASE     |")
			fmt.Println("+----------------------+------------+-----------+")
			if len(list.Items) == 0 {
				fmt.Println("| (no migrations found)                         |")
				fmt.Println("+----------------------+------------+-----------+")
				return nil
			}

			for _, item := range list.Items {
				ns := item.GetNamespace()
				name := item.GetName()

				status, _, _ := unstructured.NestedMap(item.Object, "status")
				phase, _, _ := unstructured.NestedString(status, "phase")

				fmt.Printf("| %-20s | %-10s | %-9s |\n", name, ns, phase)
			}
			fmt.Println("+----------------------+------------+-----------+")

			return nil
		},
	}

	cmd.Flags().StringVarP(&listNamespace, "namespace", "n", "", "Namespace to list PodMigrations from (defaults to global --namespace)")

	return cmd
}

