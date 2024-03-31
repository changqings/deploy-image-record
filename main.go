package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	k8scrdClient "github.com/changqings/k8scrd/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/homedir"

	appsv1 "k8s.io/api/apps/v1"
)

var (
	KUBE_USED_NS    = "kube-system|kube-public|kube-node-lease"
	not_watched_ns  string
	some_image_host string
	record_log_path = homedir.HomeDir() + "/.deploy_image_recore.log"
)

type PodUpdateRecord struct {
	ImageName string `json:"image_name"`
	OldTag    string `json:"old_tag"`
	NewTag    string `json:"new_tag"`
	UpdateAt  string `json:"update_at"`
}

func main() {

	flag.StringVar(&some_image_host, "image-host", "", "image host regexp math you want to watch, like '.*tecnent.cloudtcr.com.*'")
	flag.StringVar(&not_watched_ns, "no-ns", KUBE_USED_NS, "ns name not watched, like:"+KUBE_USED_NS)
	flag.Parse()
	slog.Info("main run...")

	if len(some_image_host) == 0 {
		slog.Error("image-host empty", "msg", "image-host can't be empty, please set image host regexp match")
		os.Exit(1)
	}

	restConfig := k8scrdClient.GetRestConfig()
	// qps limits
	restConfig.QPS = 50
	restConfig.Burst = 100

	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		panic(err.Error())
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	WatchDeploy(client, ctx.Done(), not_watched_ns)
	slog.Info("main ended")
}

func WatchDeploy(cs *kubernetes.Clientset, stopCh <-chan struct{}, notWatchedNs string) error {

	slog.Info("start run informerFactory ...")

	notWatchNsSlice := strings.Split(notWatchedNs, "|")

	// ns exclude
	informerFactory := informers.NewSharedInformerFactoryWithOptions(cs, time.Second*60, informers.WithTweakListOptions(func(lo *metav1.ListOptions) {
		lo.FieldSelector = getNotEqualFieldSelectorString(notWatchNsSlice)
	}))
	deployInformer := informerFactory.Apps().V1().Deployments()

	deployInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: handlerUpdate,
	})
	informerFactory.Start(stopCh)
	informerFactory.WaitForCacheSync(stopCh)

	slog.Info("informerFactory run success")
	<-stopCh
	slog.Info("stop informerFactory...")
	return nil

}

func handlerUpdate(oldObj, newObj interface{}) {
	oldDeploy, ok1 := oldObj.(*appsv1.Deployment)
	newDeploy, ok2 := newObj.(*appsv1.Deployment)

	if !(ok1 && ok2) {
		// slog.Error("deployInformer", "msg", "deploy informer updateFunc get deploy cr error")
		return
	}

	if oldDeploy.ResourceVersion == newDeploy.ResourceVersion {
		return
	}

	oldPodImage := getImageTag(oldDeploy)
	newPodImage := getImageTag(newDeploy)

	// fmt.Printf("name=%s,ns=%s,%v\n", oldDeploy.Name, oldDeploy.Namespace, oldPodImage)

	// first check container.name, then check image_name same
	for ok, ov := range oldPodImage {
		nv, found := newPodImage[ok]
		if found {
			oldImageTag := strings.Split(ov, ":")
			newImageTag := strings.Split(nv, ":")
			if len(oldImageTag) == 2 && len(newImageTag) == 2 && oldImageTag[1] != newImageTag[1] {
				podUpdateRecord := PodUpdateRecord{
					ImageName: oldImageTag[0],
					OldTag:    oldImageTag[1],
					NewTag:    newImageTag[1],
					UpdateAt:  time.Now().Format(time.RFC3339),
				}

				b, err := json.Marshal(podUpdateRecord)
				if err != nil {

					slog.Error("json marshal err", "msg", err)
					return
				}

				log.Println(string(b))

				// write to file
				if err := writeToFile(b, record_log_path); err != nil {
					slog.Error("write to file", "msg", err)
				}

				return
			}
		}
	}
}

func writeToFile(src []byte, path string) error {

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	defer f.Close()

	_, err = f.Write(src)
	if err != nil {
		return err
	}

	_, err = f.WriteString("\n")
	if err != nil {
		return err
	}

	return nil
}

func getImageTag(pod *appsv1.Deployment) map[string]string {

	images := make(map[string]string)
	for _, c := range pod.Spec.Template.Spec.Containers {
		matched, err := regexp.MatchString(some_image_host, c.Image)
		if err != nil {
			// slog.Error("regexp match", "msg", err)
			continue
		}
		if matched {
			images[c.Name] = c.Image
		}
	}
	return images
}

func getNotEqualFieldSelectorString(nss []string) string {
	if len(nss) == 0 {
		return ""
	}

	metadata_namespace := "metadata.namespace!="

	var result string

	for i, v := range nss {
		if i == 0 {
			result = metadata_namespace + v
			continue
		}
		result = result + "," + metadata_namespace + v

	}
	return result
}
