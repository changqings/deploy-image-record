package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	k8scrdClient "github.com/changqings/k8scrd/client"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	appsv1 "k8s.io/api/apps/v1"
)

var (
	SOME_IMAGE_HOST = ".*xx.com.*"
	// SOME_IMAGE_HOST = ".*httpbin.*"
	notWatchNs = "kube-system|kube-public|kube-node-lease"
)

type PodUpdateRecord struct {
	ImageName string `json:"image_name"`
	OldTag    string `json:"old_tag"`
	NewTag    string `json:"new_tag"`
	UpdateAt  string `json:"update_at"`
}

func main() {
	client := k8scrdClient.GetClient()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)

	defer cancel()

	WatchDeploy(client, ctx.Done(), notWatchNs)
}

func WatchDeploy(cs *kubernetes.Clientset, stopCh <-chan struct{}, notWatchedNs string) error {

	informerFactory := informers.NewSharedInformerFactory(cs, time.Second*60)
	deployInformer := informerFactory.Apps().V1().Deployments()

	notWatchNsSlice := strings.Split(notWatchedNs, "|")

	deployInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldDeploy, ok1 := oldObj.(*appsv1.Deployment)
			newDeploy, ok2 := newObj.(*appsv1.Deployment)

			if !(ok1 && ok2) {
				// slog.Error("deployInformer", "msg", "deploy informer updateFunc get deploy cr error")
				return
			}

			if oldDeploy.ResourceVersion == newDeploy.ResourceVersion {
				return
			}

			if checkNsPass(notWatchNsSlice, oldDeploy.Namespace) {
				// slog.Info("Skip", "msg", "not in watch ns", "ns", oldDeploy.Namespace, "deploy", oldDeploy.Name)
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

							// slog.Error("json marshal err", "msg", err)
							return
						}

						fmt.Println(string(b))

						return
					}
				}
			}
		},
	})

	informerFactory.Start(stopCh)
	informerFactory.WaitForCacheSync(stopCh)

	<-stopCh
	return nil

}

func getImageTag(pod *appsv1.Deployment) map[string]string {

	images := make(map[string]string)
	for _, c := range pod.Spec.Template.Spec.Containers {
		matched, err := regexp.MatchString(SOME_IMAGE_HOST, c.Image)
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

func checkNsPass(nss []string, ns string) bool {
	if len(nss) == 0 {
		return false
	}
	for _, v := range nss {
		if ns == v {
			return true
		}
	}
	return false
}
