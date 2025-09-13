package main

import (
	"encoding/json"
	"fmt"
	nested "github.com/antonfisher/nested-logrus-formatter"
	log "github.com/sirupsen/logrus"
	"xingzhan-node-autoreplace/pkg/config"
)

func init() {
	log.SetFormatter(&nested.Formatter{
		TimestampFormat: "01/02 15:04:05",
	})
}

func main() {

	//
	//config, err := clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
	//
	//if err != nil {
	//	restConfig, err := rest.InClusterConfig()
	//	if err != nil {
	//		log.Fatal(err)
	//
	//	}
	//	config = restConfig
	//
	//}
	//clientset, err := kubernetes.NewForConfig(config)
	//
	//if err != nil {
	//	log.Fatal(err)
	//}
	//
	//nirclient, err := nirclient.NewForConfig(config)
	//if err != nil {
	//	log.Error(err)
	//}
	//
	//
	//stopcha := signal.SetupSignalHandler()
	//
	//kubefactory := informers.NewSharedInformerFactory(clientset, time.Minute*0)
	//
	//nodeIssueReportFactory := nodeissuereportinformer.NewSharedInformerFactory(nirclient, time.Minute*0)
	//
	//eventInformer := kubefactory.Core().V1().Events()
	//
	//nodeIssueReportInformer := nodeIssueReportFactory.Nodeissuereporter().V1alpha1().NodeIssueReports()
	//
	//// nircontroller := controller.NewNIRController(nodeIssueReportInformer)
	//eventcontroller := controller.NewEventController(eventInformer, nodeIssueReportInformer, *clientset, *nirclient)
	//
	//// stopcha := make(chan struct{})
	//
	//kubefactory.Start(stopcha)
	//
	//
	//eventcontroller.Run(stopcha)

	toleranceColl, err := config.LoadConfiguration()

	if err != nil {
		fmt.Println(err)
	}

	marshal, err := json.Marshal(toleranceColl)

	fmt.Println(string(marshal), err)

}
