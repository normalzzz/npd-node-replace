package main

import (
	"context"
	"os"

	// "time"

	nested "github.com/antonfisher/nested-logrus-formatter"
	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"

	awspkg "xingzhan-node-autoreplace/pkg/aws"
	"xingzhan-node-autoreplace/pkg/controller"
	nirclient "xingzhan-node-autoreplace/pkg/generated/clientset/versioned"
	nodeissuereportinformer "xingzhan-node-autoreplace/pkg/generated/informers/externalversions"
	le "xingzhan-node-autoreplace/pkg/leaderelection"
	"xingzhan-node-autoreplace/pkg/signal"
)

func init() {
	log.SetFormatter(&nested.Formatter{
		TimestampFormat: "01/02 15:04:05",
	})
}

func main() {
	regionid := os.Getenv("AWS_REGION")
	cfg, err := awsconfig.LoadDefaultConfig(context.TODO(), awsconfig.WithRegion(regionid))
	if err != nil {
		log.Fatal("failed to load aws config", err)
	}
	awsOperator := awspkg.NewAwsOperator(cfg)

	config, err := clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
	if err != nil {
		restConfig, err := rest.InClusterConfig()
		if err != nil {
			log.Fatal(err)
		}
		config = restConfig
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatal(err)
	}

	nirClient, err := nirclient.NewForConfig(config)
	if err != nil {
		log.Fatal(err)
	}

	// Determine the namespace for the Lease object.
	// Use the pod's own namespace so the Lease lives alongside the workload.
	leaseNamespace := os.Getenv("SELF_POD_NAMESPACE")
	if leaseNamespace == "" {
		leaseNamespace = "default"
	}

	stopCh := signal.SetupSignalHandler()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-stopCh
		cancel()
	}()

	// Leader election: only the leader runs the controllers.
	le.Run(ctx, clientset, leaseNamespace,
		onStartedLeading(clientset, nirClient, awsOperator),
		onStoppedLeading(cancel),
	)
}

func onStartedLeading(clientset *kubernetes.Clientset, nirClient *nirclient.Clientset, awsOperator *awspkg.AwsOperator) func(ctx context.Context) {
	return func(ctx context.Context) {
		log.Infoln("became leader, starting controllers")

		kubefactory := informers.NewSharedInformerFactory(clientset, 0)
		nodeIssueReportFactory := nodeissuereportinformer.NewSharedInformerFactory(nirClient, 0)

		eventInformer := kubefactory.Core().V1().Events()
		nodeInformer := kubefactory.Core().V1().Nodes()
		nodeIssueReportInformer := nodeIssueReportFactory.Nodeissuereporter().V1alpha1().NodeIssueReports()
		toleranceConfigInformer := nodeIssueReportFactory.Nodeissuereporter().V1alpha1().ToleranceConfigs()

		eventcontroller := controller.NewEventController(eventInformer, nodeIssueReportInformer, toleranceConfigInformer, *clientset, *nirClient, nodeInformer)
		nircontroller := controller.NewNIRController(nodeIssueReportInformer, toleranceConfigInformer, *nirClient, *clientset, *awsOperator, nodeInformer)
		nodecontroller := controller.NewNodeController(nodeInformer, *nirClient, nodeIssueReportInformer)

		leaderStopCh := ctx.Done()

		kubefactory.Start(leaderStopCh)
		nodeIssueReportFactory.Start(leaderStopCh)

		go nircontroller.Run(leaderStopCh)
		go eventcontroller.Run(leaderStopCh)
		go nodecontroller.Run(leaderStopCh)

		<-leaderStopCh
		log.Infoln("leader context cancelled, controllers stopped")
	}
}

func onStoppedLeading(cancel context.CancelFunc) func() {
	return func() {
		log.Infoln("lost leadership, shutting down")
		cancel()
	}
}
