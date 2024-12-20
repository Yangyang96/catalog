package main

import (
	"errors"
	"fmt"
	"os"

	kusionapiv1 "kusionstack.io/kusion-api-go/api.kusion.io/v1"
	"kusionstack.io/kusion-module-framework/pkg/module"
)

var ErrEmptyAWSProviderRegion = errors.New("empty aws provider region")

var (
	awsRegionEnv     = "AWS_REGION"
	awsSecurityGroup = "aws_security_group"
	awsDBInstance    = "aws_db_instance"
)

var defaultAWSProviderCfg = module.ProviderConfig{
	Source:  "hashicorp/aws",
	Version: "5.0.1",
}

type awsSecurityGroupTraffic struct {
	CidrBlocks     []string `yaml:"cidr_blocks" json:"cidr_blocks"`
	Description    string   `yaml:"description" json:"description"`
	FromPort       int      `yaml:"from_port" json:"from_port"`
	IPv6CIDRBlocks []string `yaml:"ipv6_cidr_blocks" json:"ipv6_cidr_blocks"`
	PrefixListIDs  []string `yaml:"prefix_list_ids" json:"prefix_list_ids"`
	Protocol       string   `yaml:"protocol" json:"protocol"`
	SecurityGroups []string `yaml:"security_groups" json:"security_groups"`
	Self           bool     `yaml:"self" json:"self"`
	ToPort         int      `yaml:"to_port" json:"to_port"`
}

// GenerateAWSResources generates the AWS provided PostgreSQL database instance.
func (postgres *PostgreSQL) GenerateAWSResources(request *module.GeneratorRequest) ([]kusionapiv1.Resource, *kusionapiv1.Patcher, error) {
	var resources []kusionapiv1.Resource

	// Set the AWS provider with the default provider config.
	awsProviderCfg := defaultAWSProviderCfg

	// Get the AWS Terraform provider region, which should not be empty.
	var region string
	if region = module.TerraformProviderRegion(awsProviderCfg); region == "" {
		region = os.Getenv(awsRegionEnv)
	}
	if region == "" {
		return nil, nil, ErrEmptyAWSProviderRegion
	}

	// Build random_password resource.
	randomPasswordRes, randomPasswordID, err := postgres.GenerateTFRandomPassword(request)
	if err != nil {
		return nil, nil, err
	}
	resources = append(resources, *randomPasswordRes)

	// Build aws_security_group resource.
	awsSecurityGroupRes, awsSecurityGroupID, err := postgres.generateAWSSecurityGroup(awsProviderCfg, region)
	if err != nil {
		return nil, nil, err
	}
	resources = append(resources, *awsSecurityGroupRes)

	// Build aws_db_instance resource.
	awsDBInstance, awsDBInstanceID, err := postgres.generateAWSDBInstance(awsProviderCfg, region, randomPasswordID, awsSecurityGroupID)
	if err != nil {
		return nil, nil, err
	}
	resources = append(resources, *awsDBInstance)

	hostAddress := module.KusionPathDependency(awsDBInstanceID, "address")
	password := module.KusionPathDependency(randomPasswordID, "result")

	// Build Kubernetes Secret with the hostAddress, username and password of the AWS provided PostgreSQL instance,
	// and inject the credentials as the environment variable patcher.
	dbSecret, patcher, err := postgres.GenerateDBSecret(request, hostAddress, postgres.Username, password)
	if err != nil {
		return nil, nil, err
	}
	resources = append(resources, *dbSecret)

	return resources, patcher, nil
}

// generateAWSSecurityGroup generates aws_security_group resource for the AWS provided PostgreSQL database instance.
func (postgres *PostgreSQL) generateAWSSecurityGroup(awsProviderCfg module.ProviderConfig, region string) (*kusionapiv1.Resource, string, error) {
	// SecurityIPs should be in the format of IP address or Classes Inter-Domain
	// Routing (CIDR) mode.
	for _, ip := range postgres.SecurityIPs {
		if !IsIPAddress(ip) && !IsCIDR(ip) {
			return nil, "", fmt.Errorf("illegal security ip format: %s", ip)
		}
	}

	resAttrs := map[string]interface{}{
		"egress": []awsSecurityGroupTraffic{
			{
				CidrBlocks: []string{"0.0.0.0/0"},
				Protocol:   "-1",
				FromPort:   0,
				ToPort:     0,
			},
		},
		"ingress": []awsSecurityGroupTraffic{
			{
				CidrBlocks: postgres.SecurityIPs,
				Protocol:   "tcp",
				FromPort:   5432,
				ToPort:     5432,
			},
		},
	}

	id, err := module.TerraformResourceID(awsProviderCfg, awsSecurityGroup, postgres.DatabaseName+dbResSuffix)
	if err != nil {
		return nil, "", err
	}

	awsProviderCfg.ProviderMeta = map[string]any{"region": region}
	resource, err := module.WrapTFResourceToKusionResource(awsProviderCfg, awsSecurityGroup, id, resAttrs, nil)
	if err != nil {
		return nil, "", err
	}

	return resource, id, nil
}

// generateAWSDBInstance generates aws_db_instance resource for the AWS provided PostgreSQL database instance.
func (postgres *PostgreSQL) generateAWSDBInstance(awsProviderCfg module.ProviderConfig, region, randomPasswordID, awsSecurityGroupID string) (*kusionapiv1.Resource, string, error) {
	resAttrs := map[string]interface{}{
		"allocated_storage":   postgres.Size,
		"engine":              dbEngine,
		"engine_version":      postgres.Version,
		"identifier":          postgres.DatabaseName,
		"instance_class":      postgres.InstanceType,
		"password":            module.KusionPathDependency(randomPasswordID, "result"),
		"publicly_accessible": IsPublicAccessible(postgres.SecurityIPs),
		"skip_final_snapshot": true,
		"username":            postgres.Username,
		"vpc_security_group_ids": []string{
			module.KusionPathDependency(awsSecurityGroupID, "id"),
		},
	}

	if postgres.SubnetID != "" {
		resAttrs["db_subnet_group_name"] = postgres.SubnetID
	}

	id, err := module.TerraformResourceID(awsProviderCfg, awsDBInstance, postgres.DatabaseName)
	if err != nil {
		return nil, "", err
	}

	awsProviderCfg.ProviderMeta = map[string]any{"region": region}
	resource, err := module.WrapTFResourceToKusionResource(awsProviderCfg, awsDBInstance, id, resAttrs, nil)
	if err != nil {
		return nil, "", err
	}

	return resource, id, nil
}
