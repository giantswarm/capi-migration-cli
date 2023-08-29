package migrator

import (
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/giantswarm/microerror"
)

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
