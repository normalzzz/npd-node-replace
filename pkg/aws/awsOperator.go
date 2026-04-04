package awspkg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	log "github.com/sirupsen/logrus"

	//"github.com/aws/aws-sdk-go-v2/aws"
	nodeIssueReportv1alpha1 "xingzhan-node-autoreplace/pkg/apis/nodeIssueReport/v1alpha1"

	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
)

type AwsOperator struct {
	ec2client *ec2.Client
	asgclient *autoscaling.Client
	snscli    *sns.Client
}

var boolvar = false

func (a *AwsOperator) RebootInstance(InstanceId string) error {
	//var boolva = false
	ec2input := ec2.RebootInstancesInput{
		InstanceIds: []string{InstanceId},
		DryRun:      &boolvar,
	}
	instancesinfo, err := a.ec2client.RebootInstances(context.Background(), &ec2input)
	if err != nil {
		log.Errorln("faile to reboot instance:", InstanceId)
		return err
	}
	log.Infoln("rebooted instance:", InstanceId)

	instanceinfojson, err := json.Marshal(instancesinfo)
	if err != nil {
		log.Errorln("faile to marshal instance info:", InstanceId)
		return nil
	}
	log.Debugln(string(instanceinfojson))
	return nil
}

func (a *AwsOperator) DetachInstance(asgId string, instanceId string) error {
	// TODO: add detachm logic

	detachinstanceInput := autoscaling.DetachInstancesInput{
		AutoScalingGroupName:           &asgId,
		InstanceIds:                    []string{instanceId},
		ShouldDecrementDesiredCapacity: &boolvar,
	}
	detachInstanceResult, err := a.asgclient.DetachInstances(context.Background(), &detachinstanceInput)

	if err != nil {
		return err
	}
	log.Infoln("successfully detached instance:", instanceId)
	// ignored this detachedInstance output Marshal, may be lead to some failure
	detachInstanceMarshal, _ := json.Marshal(detachInstanceResult)
	log.Debugln("detachInstanceResult:", string(detachInstanceMarshal))
	return nil
}

func (a *AwsOperator) GetASGId(instanceid string) (string, error) {
	describeinstanceInput := ec2.DescribeInstancesInput{
		DryRun:      &boolvar,
		InstanceIds: []string{instanceid},
	}

	describeinstancesout, err := a.ec2client.DescribeInstances(context.Background(), &describeinstanceInput)
	if err != nil {
		log.Errorln("faile to describe instance:", instanceid, "with error:", err)
	}
	asgname := ""
	tags := describeinstancesout.Reservations[0].Instances[0].Tags

	for _, tag := range tags {
		if *tag.Key == "aws:autoscaling:groupName" {
			asgname = *tag.Value
		}
	}
	if asgname == "" {

		return "", errors.New("faile to find ASG name from instance tag")
	}
	return asgname, nil

	//return ""
}

func (a *AwsOperator) SNSNotify(nodeissuereport nodeIssueReportv1alpha1.NodeIssueReport, reason string) error {
	snstopic := os.Getenv("SNS_TOPIC_ARN")
	action := nodeissuereport.Spec.Action
	nodeProblems := nodeissuereport.Spec.NodeProblems
	nodeProblemsJson, err := json.Marshal(nodeProblems)
	if err != nil {
		log.Errorln(" while try to publish node issue through sns, Marshal nodeProblems object , error happened:", err)
		return err
	}
	fulljson, err := json.Marshal(nodeissuereport)
	if err != nil {
		log.Errorln(" while try to publish node issue through sns, Marshal full nodeissuereport object , error happened:", err)
		return err
	}

	var snsSubject string
	switch reason {
	case "reboot":
		snsSubject = "[npd-node-replace] Node REBOOTED due to persistent issues"
	case "replace":
		snsSubject = "[npd-node-replace] Node REPLACED due to persistent issues"
	case "paging":
		snsSubject = "[npd-node-replace] Node issues detected - admin notification (paging)"
	case "not-allowed":
		snsSubject = "[npd-node-replace] Node issues detected - auto-action disabled, notify only"
	case "cooldown-expired":
		snsSubject = "[npd-node-replace] Node issue report cleanup - cooldown expired, no escalation triggered"
	case "escalate-paging":
		snsSubject = "[npd-node-replace] ESCALATION - repeated issues after action, admin notification"
	default:
		snsSubject = fmt.Sprintf("[npd-node-replace] Node issue notification (%s)", reason)
	}

	nodeissuereportmessage := fmt.Sprintf(`
NodeName: %s
NodeStatus: %s
Issues Detected: %s
Action: %s
Escalated: %v
ScoreInBucket: %d
Reason: %s
Full NodeIssueReport: %s
`, nodeissuereport.Spec.NodeName, nodeissuereport.Spec.NodeStatus, string(nodeProblemsJson), action, nodeissuereport.Spec.Escalated, nodeissuereport.Spec.ScoreInBucket, reason, string(fulljson))

	snspublishInput := sns.PublishInput{
		TopicArn: &snstopic,
		Subject:  &snsSubject,
		Message:  &nodeissuereportmessage,
	}
	_, err = a.snscli.Publish(context.TODO(), &snspublishInput)
	if err != nil {
		log.Errorln("failed to send SNS notification with error", err)
		return err
	}
	return nil
}

func NewAwsOperator(config aws.Config) *AwsOperator {

	ec2cli := ec2.NewFromConfig(config)
	asgcli := autoscaling.NewFromConfig(config)
	snscli := sns.NewFromConfig(config)
	return &AwsOperator{
		ec2client: ec2cli,
		asgclient: asgcli,
		snscli:    snscli,
	}
}
