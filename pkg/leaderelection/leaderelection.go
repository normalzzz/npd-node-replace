package leaderelection

import (
	"context"
	"os"
	"time"

	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

const (
	leaseName = "npd-node-replace-leader"
)

// Run starts leader election. The onStartedLeading callback is invoked when
// this instance becomes the leader; onStoppedLeading is called when leadership
// is lost. The function blocks until ctx is cancelled or leadership is lost.
func Run(ctx context.Context, clientset kubernetes.Interface, namespace string, onStartedLeading func(ctx context.Context), onStoppedLeading func()) {
	id := podIdentity()

	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      leaseName,
			Namespace: namespace,
		},
		Client: clientset.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: id,
		},
	}

	log.Infof("starting leader election, identity=%s, namespace=%s", id, namespace)

	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:            lock,
		LeaseDuration:   15 * time.Second,
		RenewDeadline:   10 * time.Second,
		RetryPeriod:     2 * time.Second,
		ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: onStartedLeading,
			OnStoppedLeading: onStoppedLeading,
			OnNewLeader: func(identity string) {
				if identity == id {
					return
				}
				log.Infof("new leader elected: %s", identity)
			},
		},
	})
}

func podIdentity() string {
	// Use pod name as unique identity; fall back to hostname
	if name := os.Getenv("SELF_POD_NAME"); name != "" {
		return name
	}
	h, _ := os.Hostname()
	return h
}
