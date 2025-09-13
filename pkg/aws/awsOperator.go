package awspkg

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	log "github.com/sirupsen/logrus"
	//"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
)

type AwsOperator struct {
	ec2client *ec2.Client
	asgclient *autoscaling.Client
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
		log.Errorln("faile to describe instance:", instanceid)
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

func NewAwsOperator(config aws.Config) *AwsOperator {

	ec2cli := ec2.NewFromConfig(config)
	asgcli := autoscaling.NewFromConfig(config)
	return &AwsOperator{
		ec2client: ec2cli,
		asgclient: asgcli,
	}
}
