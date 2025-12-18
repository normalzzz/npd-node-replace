package main

import (
	"context"
	nested "github.com/antonfisher/nested-logrus-formatter"
	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	// "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"time"
	awspkg "xingzhan-node-autoreplace/pkg/aws"
	"xingzhan-node-autoreplace/pkg/controller"
	nirclient "xingzhan-node-autoreplace/pkg/generated/clientset/versioned"
	nodeissuereportinformer "xingzhan-node-autoreplace/pkg/generated/informers/externalversions"
	"xingzhan-node-autoreplace/pkg/signal"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"os"
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

	nirclient, err := nirclient.NewForConfig(config)
	if err != nil {
		log.Error(err)
	}

	// initialize stopcha
	stopcha := signal.SetupSignalHandler()

	kubefactory := informers.NewSharedInformerFactory(clientset, time.Minute*0)

	nodeIssueReportFactory := nodeissuereportinformer.NewSharedInformerFactory(nirclient, time.Minute*0)

	eventInformer := kubefactory.Core().V1().Events()

	nodeInformer := kubefactory.Core().V1().Nodes()

	nodeIssueReportInformer := nodeIssueReportFactory.Nodeissuereporter().V1alpha1().NodeIssueReports()

	// nircontroller := controller.NewNIRController(nodeIssueReportInformer)
	eventcontroller := controller.NewEventController(eventInformer, nodeIssueReportInformer, *clientset, *nirclient, nodeInformer)

	nircontroller := controller.NewNIRController(nodeIssueReportInformer, *nirclient, *clientset, *awsOperator, nodeInformer)
	// stopcha := make(chan struct{})
	nodecontroller := controller.NewNodeController(nodeInformer, *nirclient, nodeIssueReportInformer)

	kubefactory.Start(stopcha)
	nodeIssueReportFactory.Start(stopcha)

	go nircontroller.Run(stopcha)
	go eventcontroller.Run(stopcha)
	go nodecontroller.Run(stopcha)


	<-stopcha

	log.Infoln("Main program received stop signal, shutting down")
	//awsOperator := awspkg.NewAwsOperator(cfg)


}
