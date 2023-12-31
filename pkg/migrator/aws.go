package migrator

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/elb"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/giantswarm/backoff"
	"github.com/giantswarm/microerror"
)

func (s *Service) CreateAWSClients(awsSession *session.Session) {
	s.ec2Client = ec2.New(awsSession)
	s.elbClient = elb.New(awsSession)
	s.route53Client = route53.New(awsSession)
	s.asgClient = autoscaling.New(awsSession)
}

func (s *Service) refreshAWSClients() error {
	var refreshed bool
	var err error
	refreshClients := func() error {
		refreshed, err = s.clusterInfo.RefreshAWSCredentialsIfExpired()
		if err != nil {
			return microerror.Mask(err)
		}
		return nil
	}
	err = backoff.Retry(refreshClients, s.backOff)
	if err != nil {
		return microerror.Mask(err)
	}
	if refreshed {
		s.CreateAWSClients(s.clusterInfo.AWSSession)
	}
	return nil
}

func (s *Service) getMasterSecurityGroupID() (string, error) {
	var err error
	var masterSecurityGroupID string

	i := &ec2.DescribeSecurityGroupsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("tag:Name"),
				Values: aws.StringSlice([]string{fmt.Sprintf("%s-master", s.clusterInfo.Name)}),
			},
		},
	}
	o, err := s.ec2Client.DescribeSecurityGroups(i)
	if err != nil {
		return "", microerror.Mask(err)
	}
	if len(o.SecurityGroups) != 1 {
		return "", microerror.Maskf(nil, "expected 1 master security group but found %d", len(o.SecurityGroups))
	}
	masterSecurityGroupID = *o.SecurityGroups[0].GroupId

	return masterSecurityGroupID, nil
}

func (s *Service) getWorkerSecurityGroupID(workerName string) (string, error) {
	i := &ec2.DescribeSecurityGroupsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("tag:Name"),
				Values: aws.StringSlice([]string{fmt.Sprintf("%s-worker", s.clusterInfo.Name)}),
			},
			{
				Name:   aws.String("tag:giantswarm.io/machine-deployment"),
				Values: aws.StringSlice([]string{workerName}),
			},
		},
	}

	o, err := s.ec2Client.DescribeSecurityGroups(i)
	if err != nil {
		return "", microerror.Mask(err)
	}
	if len(o.SecurityGroups) != 1 {
		return "", microerror.Maskf(nil, "expected 1 worker security group but found %d", len(o.SecurityGroups))
	}

	return *o.SecurityGroups[0].GroupId, nil
}

func (s *Service) getInternetGatewayID() (string, error) {
	i := &ec2.DescribeInternetGatewaysInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("tag:giantswarm.io/cluster"),
				Values: aws.StringSlice([]string{s.clusterInfo.Name}),
			},
		},
	}

	o, err := s.ec2Client.DescribeInternetGateways(i)
	if err != nil {
		return "", microerror.Mask(err)
	}
	if len(o.InternetGateways) != 1 {
		return "", microerror.Maskf(nil, "expected 1 internet gateway but found %d", len(o.InternetGateways))
	}

	return *o.InternetGateways[0].InternetGatewayId, nil
}

func (s *Service) getSubnetsInVPC(vpcID string) ([]*ec2.Subnet, error) {
	i := &ec2.DescribeSubnetsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: aws.StringSlice([]string{vpcID}),
			},
		},
	}

	o, err := s.ec2Client.DescribeSubnets(i)
	if err != nil {
		return nil, microerror.Mask(err)
	}

	return o.Subnets, nil
}

func (s *Service) getRouteTableForSubnet(subnetID string) (string, error) {
	i := &ec2.DescribeRouteTablesInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("association.subnet-id"),
				Values: aws.StringSlice([]string{subnetID}),
			},
		},
	}

	o, err := s.ec2Client.DescribeRouteTables(i)
	if err != nil {
		return "", microerror.Mask(err)
	}
	if len(o.RouteTables) != 1 {
		return "", microerror.Maskf(nil, "expected 1 route table but found %d", len(o.RouteTables))
	}

	return *o.RouteTables[0].RouteTableId, nil
}

func (s *Service) getNatGatewayForSubnet(subnetID string) (string, error) {
	i := &ec2.DescribeNatGatewaysInput{
		Filter: []*ec2.Filter{
			{
				Name:   aws.String("subnet-id"),
				Values: aws.StringSlice([]string{subnetID}),
			},
		},
	}
	o, err := s.ec2Client.DescribeNatGateways(i)
	if err != nil {
		return "", microerror.Mask(err)
	}
	if len(o.NatGateways) == 0 {
		return "", nil
	} else if len(o.NatGateways) != 1 {
		return "", microerror.Maskf(nil, "expected 1 nat gateway but found %d", len(o.NatGateways))
	}

	return *o.NatGateways[0].NatGatewayId, nil
}

func (s *Service) getSubnets(vpcID string) ([]Subnet, error) {
	var subnets []Subnet
	var err error

	ec2Subnets, err := s.getSubnetsInVPC(vpcID)
	if err != nil {
		return nil, microerror.Mask(err)
	}

	for _, ec2Subnet := range ec2Subnets {
		routeTableID, err := s.getRouteTableForSubnet(*ec2Subnet.SubnetId)
		if err != nil {
			return nil, microerror.Mask(err)
		}
		natGatewayID, err := s.getNatGatewayForSubnet(*ec2Subnet.SubnetId)
		if err != nil {
			return nil, microerror.Mask(err)
		}

		subnet := Subnet{
			ID:           *ec2Subnet.SubnetId,
			RouteTableID: routeTableID,
			IsPublic:     false,
		}
		// if subnet has assigned  NAT gateway then it is a public subnet
		if natGatewayID != "" {
			subnet.NatGatewayID = natGatewayID
			subnet.IsPublic = true
		}
		subnets = append(subnets, subnet)
	}

	return subnets, nil
}

func (s *Service) addCAPIControlPlaneNodesToVintageELBs() error {
	var instanceIDs []string
	counter := 0
	for {
		// get instance IDS with tags
		i := &ec2.DescribeInstancesInput{
			Filters: []*ec2.Filter{
				{
					Name:   aws.String(fmt.Sprintf("tag:sigs.k8s.io/cluster-api-provider-aws/cluster/%s", s.clusterInfo.Name)),
					Values: aws.StringSlice([]string{"owned"}),
				},
				{
					Name:   aws.String("tag:sigs.k8s.io/cluster-api-provider-aws/role"),
					Values: aws.StringSlice([]string{"control-plane"}),
				},
			},
		}

		o, err := s.ec2Client.DescribeInstances(i)
		if err != nil {
			return microerror.Mask(err)
		}
		for _, r := range o.Reservations {
			for _, i := range r.Instances {
				instanceIDs = append(instanceIDs, *i.InstanceId)
			}
		}

		if len(instanceIDs) > 0 {
			break
		}
		time.Sleep(time.Second * 5)
		counter += 5
	}

	elbNames := []string{fmt.Sprintf("%s-api", s.clusterInfo.Name), fmt.Sprintf("%s-api-internal", s.clusterInfo.Name)}

	for _, lb := range elbNames {

		i := &elb.RegisterInstancesWithLoadBalancerInput{
			LoadBalancerName: aws.String(lb),
		}
		for _, id := range instanceIDs {
			alreadyRegistered, err := s.isInstanceRegisteredWithLoadbalancer(id, lb)
			if err != nil {
				return microerror.Mask(err)
			}
			if alreadyRegistered {
				continue
			} else {
				fmt.Printf("Registering instance %s with ELB %s\n", id, lb)
			}

			i.Instances = append(i.Instances, &elb.Instance{
				InstanceId: aws.String(id),
			})
		}
		if len(i.Instances) == 0 {
			continue
		}

		_, err := s.elbClient.RegisterInstancesWithLoadBalancer(i)
		if err != nil {
			return microerror.Mask(err)
		}
	}

	return nil
}

func (s *Service) isInstanceRegisteredWithLoadbalancer(instanceID string, elbName string) (bool, error) {
	// describe elb and check if instance is registered
	i := &elb.DescribeLoadBalancersInput{
		LoadBalancerNames: aws.StringSlice([]string{elbName}),
	}
	o, err := s.elbClient.DescribeLoadBalancers(i)
	if err != nil {
		return false, microerror.Mask(err)
	}
	for _, i := range o.LoadBalancerDescriptions[0].Instances {
		if *i.InstanceId == instanceID {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) deleteVintageASGGroups(filters []*autoscaling.Filter) error {
	var err error
	f := []*autoscaling.Filter{
		{
			Name: aws.String("tag:giantswarm.io/cluster"),
			Values: []*string{
				aws.String(s.clusterInfo.Name),
			},
		},
	}
	f = append(f, filters...)

	i := autoscaling.DescribeAutoScalingGroupsInput{
		Filters: f,
	}

	var out *autoscaling.DescribeAutoScalingGroupsOutput
	describeASG := func() error {
		out, err = s.asgClient.DescribeAutoScalingGroups(&i)
		if err != nil {
			return microerror.Mask(err)
		}
		return nil
	}
	err = backoff.Retry(describeASG, s.backOff)
	if err != nil {
		return microerror.Mask(err)
	}

	fmt.Printf("Found %d ASG groups for deletion\n", len(out.AutoScalingGroups))

	for _, asg := range out.AutoScalingGroups {

		i := autoscaling.DeleteAutoScalingGroupInput{
			AutoScalingGroupName: asg.AutoScalingGroupName,
			ForceDelete:          aws.Bool(true),
		}

		deleteASG := func() error {
			_, err := s.asgClient.DeleteAutoScalingGroup(&i)
			if err != nil {
				return microerror.Mask(err)
			}
			return nil
		}
		err = backoff.Retry(deleteASG, s.backOff)
		if err != nil {
			return microerror.Mask(err)
		}
		fmt.Printf("Deleted ASG group %s\n", *asg.AutoScalingGroupName)

		var instanceIDs []*string
		for _, instance := range asg.Instances {
			instanceIDs = append(instanceIDs, instance.InstanceId)
		}

		// terminate each instance in the ASG
		i2 := &ec2.TerminateInstancesInput{
			InstanceIds: instanceIDs,
		}
		terminateInstances := func() error {
			_, err = s.ec2Client.TerminateInstances(i2)
			if err != nil {
				return microerror.Mask(err)
			}
			return nil
		}
		err = backoff.Retry(terminateInstances, s.backOff)
		if err != nil {
			return microerror.Mask(err)
		}
		fmt.Printf("Terminated %d instances in ASG group %s\n", len(instanceIDs), *asg.AutoScalingGroupName)

	}

	return nil
}

func tccpnAsgFilters() []*autoscaling.Filter {
	return []*autoscaling.Filter{
		{
			Name: aws.String("tag:giantswarm.io/stack"),
			Values: []*string{
				aws.String("tccpn"),
			},
		},
	}
}

func tcnpAsgFilters(nodePoolName string) []*autoscaling.Filter {
	return []*autoscaling.Filter{
		{
			Name: aws.String("tag:giantswarm.io/stack"),
			Values: []*string{
				aws.String("tcnp"),
			},
		},
		{
			Name: aws.String("tag:giantswarm.io/machine-deployment"),
			Values: []*string{
				aws.String(nodePoolName),
			},
		},
	}
}
