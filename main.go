package main

import (
	"context"
	nested "github.com/antonfisher/nested-logrus-formatter"
	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
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

	stopcha := signal.SetupSignalHandler()

	kubefactory := informers.NewSharedInformerFactory(clientset, time.Minute*0)

	nodeIssueReportFactory := nodeissuereportinformer.NewSharedInformerFactory(nirclient, time.Minute*0)

	eventInformer := kubefactory.Core().V1().Events()

	nodeIssueReportInformer := nodeIssueReportFactory.Nodeissuereporter().V1alpha1().NodeIssueReports()

	// nircontroller := controller.NewNIRController(nodeIssueReportInformer)
	eventcontroller := controller.NewEventController(eventInformer, nodeIssueReportInformer, *clientset, *nirclient)

	// stopcha := make(chan struct{})

	kubefactory.Start(stopcha)

	eventcontroller.Run(stopcha)

	awspkg.NewAwsOperator()

	nircontroller := controller.NewNIRController(nodeIssueReportInformer, *nirclient, *clientset, awsOperator)

	nircontroller.Run(stopcha)

	//toleranceColl, err := config.LoadConfiguration()
	//
	//if err != nil {
	//	fmt.Println(err)
	//}
	//
	//marshal, err := json.Marshal(toleranceColl)
	//
	//fmt.Println(string(marshal), err)

}
