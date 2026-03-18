// Package k8s handles Kubernetes API interactions: pod discovery, metrics
// retrieval, API server proxy for Connect REST, and exec for JVM inspection.
package k8s

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dougdalo/kcdiag/internal/config"
	"github.com/dougdalo/kcdiag/pkg/models"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/homedir"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
)

// Client wraps the Kubernetes API with kcdiag-specific operations.
type Client struct {
	clientset     kubernetes.Interface
	metricsClient metricsclient.Interface
	restConfig    *rest.Config // stored for exec/SPDY
	cfg           *config.Config
}

// NewClient creates a Kubernetes client from kubeconfig or in-cluster config.
func NewClient(cfg *config.Config) (*Client, error) {
	restCfg, err := buildRESTConfig(cfg.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("kubernetes config: %w", err)
	}

	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}

	mc, err := metricsclient.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("metrics client: %w", err)
	}

	return &Client{
		clientset:     cs,
		metricsClient: mc,
		restConfig:    restCfg,
		cfg:           cfg,
	}, nil
}

// buildRESTConfig resolves kubeconfig with precedence:
// --kubeconfig flag > $KUBECONFIG env > ~/.kube/config > in-cluster.
func buildRESTConfig(kubeconfig string) (*rest.Config, error) {
	// 1. Explicit flag
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}

	// 2. $KUBECONFIG environment variable
	if env := os.Getenv("KUBECONFIG"); env != "" {
		return clientcmd.BuildConfigFromFlags("", env)
	}

	// 3. Default ~/.kube/config
	if home := homedir.HomeDir(); home != "" {
		candidate := filepath.Join(home, ".kube", "config")
		cfg, err := clientcmd.BuildConfigFromFlags("", candidate)
		if err == nil {
			return cfg, nil
		}
	}

	// 4. In-cluster service account
	return rest.InClusterConfig()
}

// DiscoverConnectPods finds Kafka Connect pods using Strimzi label selectors.
func (c *Client) DiscoverConnectPods(ctx context.Context) ([]models.PodInfo, error) {
	var allPods []models.PodInfo

	namespaces := c.cfg.Namespaces
	if len(namespaces) == 0 || (len(namespaces) == 1 && namespaces[0] == "") {
		namespaces = []string{""}
	}

	for _, ns := range namespaces {
		pods, err := c.clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
			LabelSelector: c.cfg.Labels,
		})
		if err != nil {
			return nil, fmt.Errorf("list pods in namespace %q: %w", ns, err)
		}

		for i := range pods.Items {
			allPods = append(allPods, podToPodInfo(&pods.Items[i], c.cfg.ConnectPort))
		}
	}

	return allPods, nil
}

// GetPodMetrics retrieves memory/CPU from metrics-server and enriches pods in-place.
// Returns nil on metrics-server errors so callers can continue without metrics.
func (c *Client) GetPodMetrics(ctx context.Context, pods []models.PodInfo) error {
	byNS := make(map[string][]int)
	for i, p := range pods {
		byNS[p.Namespace] = append(byNS[p.Namespace], i)
	}

	for ns, indices := range byNS {
		list, err := c.metricsClient.MetricsV1beta1().PodMetricses(ns).List(ctx, metav1.ListOptions{
			LabelSelector: c.cfg.Labels,
		})
		if err != nil {
			fmt.Printf("warning: metrics-server unavailable for ns %q: %v\n", ns, err)
			continue
		}

		metricsMap := buildMetricsMap(list)
		for _, idx := range indices {
			if m, ok := metricsMap[pods[idx].Name]; ok {
				pods[idx].MemoryUsage = m.memoryBytes
				pods[idx].CPUUsage = m.cpuMillis
				if pods[idx].MemoryLimit > 0 {
					pods[idx].MemoryPercent = float64(m.memoryBytes) / float64(pods[idx].MemoryLimit) * 100
				}
			}
		}
	}

	return nil
}

// ProxyGet tunnels an HTTP GET to a pod through the Kubernetes API server proxy.
// Path: /api/v1/namespaces/{ns}/pods/{pod}:{port}/proxy/{path}
//
// This is the key method for bastion-host execution: the bastion can reach
// the K8s API server but NOT the pod network (10.x.x.x). The API server
// proxies the request to the pod on our behalf.
func (c *Client) ProxyGet(ctx context.Context, namespace, podName string, port int, path string) ([]byte, error) {
	result := c.clientset.CoreV1().RESTClient().Get().
		Namespace(namespace).
		Resource("pods").
		Name(fmt.Sprintf("%s:%d", podName, port)).
		SubResource("proxy").
		Suffix(path).
		Do(ctx)

	if err := result.Error(); err != nil {
		return nil, fmt.Errorf("proxy GET %s/%s:%d/%s: %w", namespace, podName, port, path, err)
	}

	body, err := result.Raw()
	if err != nil {
		return nil, fmt.Errorf("proxy GET %s/%s:%d/%s read body: %w", namespace, podName, port, path, err)
	}

	return body, nil
}

// ExecResult holds stdout/stderr from a command run inside a pod.
type ExecResult struct {
	Stdout string
	Stderr string
}

// resolveContainer returns a valid container name for the given pod.
// If container is non-empty and exists in the pod spec, it is returned as-is.
// If container is empty or not found, the first container in the pod spec is used.
func (c *Client) resolveContainer(ctx context.Context, namespace, podName, container string) (string, error) {
	pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get pod %s/%s: %w", namespace, podName, err)
	}
	if len(pod.Spec.Containers) == 0 {
		return "", fmt.Errorf("pod %s/%s has no containers", namespace, podName)
	}

	// If a container name was provided, check if it exists.
	if container != "" {
		for _, c := range pod.Spec.Containers {
			if c.Name == container {
				return container, nil
			}
		}
		// Requested container not found — fall back to first container.
		fmt.Printf("warning: container %q not found in pod %s/%s, using %q instead\n",
			container, namespace, podName, pod.Spec.Containers[0].Name)
	}

	return pod.Spec.Containers[0].Name, nil
}

// ExecInPod runs a command inside a container via SPDY exec.
// Used for deep JVM inspection (jcmd, jmap, thread dump).
// If container is empty or not found in the pod, the first container is used.
func (c *Client) ExecInPod(ctx context.Context, namespace, podName, container string, command []string) (*ExecResult, error) {
	resolved, err := c.resolveContainer(ctx, namespace, podName, container)
	if err != nil {
		return nil, err
	}

	req := c.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: resolved,
			Command:   command,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(c.restConfig, "POST", req.URL())
	if err != nil {
		return nil, fmt.Errorf("create SPDY executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	if err := exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	}); err != nil {
		return &ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}, err
	}

	return &ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}, nil
}

// --- internal helpers ---

type podMetric struct {
	memoryBytes int64
	cpuMillis   int64
}

func buildMetricsMap(list *metricsv1beta1.PodMetricsList) map[string]podMetric {
	m := make(map[string]podMetric, len(list.Items))
	for _, pm := range list.Items {
		var mem, cpu int64
		for _, c := range pm.Containers {
			if v, ok := c.Usage[corev1.ResourceMemory]; ok {
				mem += v.Value()
			}
			if v, ok := c.Usage[corev1.ResourceCPU]; ok {
				cpu += v.MilliValue()
			}
		}
		m[pm.Name] = podMetric{memoryBytes: mem, cpuMillis: cpu}
	}
	return m
}

func podToPodInfo(pod *corev1.Pod, connectPort int) models.PodInfo {
	info := models.PodInfo{
		Name:      pod.Name,
		Namespace: pod.Namespace,
		NodeName:  pod.Spec.NodeName,
		Labels:    pod.Labels,
		IP:        pod.Status.PodIP,
	}

	if cluster, ok := pod.Labels["strimzi.io/cluster"]; ok {
		info.ClusterName = cluster
	}

	if info.IP != "" {
		info.ConnectURL = fmt.Sprintf("http://%s:%d", info.IP, connectPort)
	}

	// Strimzi uses a single kafka-connect container; grab its memory limit.
	for _, c := range pod.Spec.Containers {
		if limit, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
			info.MemoryLimit = limit.Value()
		} else if req, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
			info.MemoryLimit = req.Value()
		}
		break
	}

	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			info.Ready = true
		}
	}

	for _, cs := range pod.Status.ContainerStatuses {
		info.RestartCount += cs.RestartCount
	}

	return info
}
