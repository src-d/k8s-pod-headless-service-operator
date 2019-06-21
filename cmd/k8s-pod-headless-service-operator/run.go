package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/apex/log"

	gocli "gopkg.in/src-d/go-cli.v0"
	core_v1 "k8s.io/api/core/v1"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

func init() {
	app.AddCommand(&RunCommand{})
}

type RunCommand struct {
	gocli.PlainCommand `name:"run" short-description:"run a watcher for " long-description:"Run an in-cluster watcher for PVs and create the needed paths if needed"`
	KubernetesContext  string `long:"context" env:"KUBERNETES_CONTEXT" description:"If set the program will load the kubernetes configuration from a kubeconfig file for the given context"`
	Namespace          string `long:"namespace" env:"NAMESPACE" default:"" description:"Namespace to watch, defaults to all"`
	Annotation         string `long:"pod-annotation" env:"POD_ANNOTATION" default:"srcd.host/create-headless-service" description:"annotation that needs to be set to 'true' for the service to be created"`
	clientSet          *kubernetes.Clientset
}

func (r *RunCommand) ExecuteContext(ctx context.Context, args []string) error {
	var err error
	r.clientSet, err = r.getClientSet()
	if err != nil {
		return err
	}
	podInformer := coreinformers.NewPodInformer(r.clientSet, r.Namespace, time.Minute, cache.Indexers{})

	podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			err := r.setUpService(obj.(*core_v1.Pod))
			if err != nil {
				log.Infof("Error setting up service: %s", err)
			}
		},
		UpdateFunc: func(oldObj interface{}, newObj interface{}) {
			err := r.updateService(newObj.(*core_v1.Pod))
			if err != nil {
				log.Infof("Error updating service: %s", err)
			}
		},
		DeleteFunc: func(obj interface{}) {
			err := r.deleteService(obj.(*core_v1.Pod))
			if err != nil {
				log.Infof("Error deleting service: %s", err)
			}
		},
	})

	stop := make(chan struct{})
	defer close(stop)
	go podInformer.Run(stop)

	log.Info("Watching pods")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	<-sig
	stop <- struct{}{}

	return nil
}

func (r *RunCommand) getClientSet() (*kubernetes.Clientset, error) {
	if r.clientSet != nil {
		return r.clientSet, nil

	}

	var config *rest.Config
	var err error
	if r.KubernetesContext != "" {
		config, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(),
			&clientcmd.ConfigOverrides{
				CurrentContext: r.KubernetesContext,
			},
		).ClientConfig()
	} else {
		config, err = rest.InClusterConfig()
	}

	if err != nil {
		return nil, err
	}

	return kubernetes.NewForConfig(config)
}

func (r *RunCommand) hasExistingService(pod *core_v1.Pod) bool {
	client, err := r.getClientSet()
	if err != nil {
		return false
	}

	_, err = client.CoreV1().Services(pod.GetNamespace()).Get(pod.GetName(), meta_v1.GetOptions{})
	return err == nil
}

func (r *RunCommand) updateService(pod *core_v1.Pod) error {
	log.Infof("Updating pod %s", pod.ObjectMeta.Name)
	if pod.Annotations[r.Annotation] != "true" {
		log.Infof("%s doesn't have annotation set, skipping", pod.ObjectMeta.Name)
		return nil
	}

	if pod.Status.PodIP == "" {
		log.Infof("%s doesn't have an IP yet skipping", pod.ObjectMeta.Name)
		return nil
	}

	client, err := r.getClientSet()
	if err != nil {
		return err
	}

	if !r.hasExistingService(pod) {
		log.Infof("%s has no service, creating it", pod.ObjectMeta.Name)
		return r.setUpService(pod)
	}

	endpoint, err := client.CoreV1().Endpoints(pod.GetNamespace()).Get(pod.GetName(), meta_v1.GetOptions{})
	if err != nil {
		return err
	}

	if len(endpoint.Subsets) == 0 || len(endpoint.Subsets[0].Addresses) == 0 || endpoint.Subsets[0].Addresses[0].IP != pod.Status.PodIP {
		log.Infof("%s has a new Pod IP, updating it", pod.ObjectMeta.Name)
		// update the pod IP
		_, err = client.CoreV1().Endpoints(pod.GetNamespace()).Update(&core_v1.Endpoints{
			ObjectMeta: meta_v1.ObjectMeta{
				Name:        pod.GetObjectMeta().GetName(),
				Annotations: pod.GetAnnotations(),
			},
			Subsets: []core_v1.EndpointSubset{
				core_v1.EndpointSubset{
					Addresses: []core_v1.EndpointAddress{
						core_v1.EndpointAddress{
							IP: pod.Status.PodIP,
						},
					},
				},
			},
		})

		if err != nil {
			return err
		}
	}

	return nil
}

func (r *RunCommand) setUpService(pod *core_v1.Pod) error {
	log.Infof("Setting up pod %s", pod.ObjectMeta.Name)
	if pod.Annotations[r.Annotation] != "true" {
		log.Infof("%s doesn't have annotation set, skipping", pod.ObjectMeta.Name)
		return nil
	}

	if pod.Status.PodIP == "" {
		log.Infof("%s doesn't have an IP yet skipping", pod.ObjectMeta.Name)
		return nil
	}

	if r.hasExistingService(pod) {
		log.Infof("%s already has a service, updating it", pod.ObjectMeta.Name)
		return r.updateService(pod)
	}

	client, err := r.getClientSet()
	if err != nil {
		return err
	}

	_, err = client.CoreV1().Services(pod.GetNamespace()).Create(&core_v1.Service{
		ObjectMeta: meta_v1.ObjectMeta{
			Name:        pod.GetObjectMeta().GetName(),
			Annotations: pod.GetAnnotations(),
		},
		Spec: core_v1.ServiceSpec{
			ClusterIP: "None", // headless service
		},
	})

	if err != nil {
		return err
	}

	// endpoints is needed as a Service selector will select all replicas in a replicaset
	_, err = client.CoreV1().Endpoints(pod.GetNamespace()).Create(&core_v1.Endpoints{
		ObjectMeta: meta_v1.ObjectMeta{
			Name:        pod.GetObjectMeta().GetName(),
			Annotations: pod.GetAnnotations(),
		},
		Subsets: []core_v1.EndpointSubset{
			core_v1.EndpointSubset{
				Addresses: []core_v1.EndpointAddress{
					core_v1.EndpointAddress{
						IP: pod.Status.PodIP,
					},
				},
			},
		},
	})

	return err
}

func (r *RunCommand) deleteService(pod *core_v1.Pod) error {
	log.Infof("Deleting service for pod %s", pod.ObjectMeta.Name)
	if pod.Annotations[r.Annotation] != "true" {
		log.Infof("%s doesn't have annotation set, skipping", pod.ObjectMeta.Name)
		return nil
	}

	if !r.hasExistingService(pod) {
		log.Infof("%s does not have a service, skipping", pod.ObjectMeta.Name)
		return r.updateService(pod)
	}

	client, err := r.getClientSet()
	if err != nil {
		return err
	}

	err = client.CoreV1().Services(pod.GetNamespace()).Delete(pod.GetName(), &meta_v1.DeleteOptions{})
	if err != nil {
		return err
	}

	return client.CoreV1().Endpoints(pod.GetNamespace()).Delete(pod.GetName(), &meta_v1.DeleteOptions{})
}
