package main

import (
	"context"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"time"
	"os"
	"strings"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric/instrument"
	"go.opentelemetry.io/otel/metric/instrument/syncfloat64"
	"go.opentelemetry.io/otel/sdk/metric"
)

type config struct {
	instanceShape         string
	instanceName          string
	instanceImage         string
	instanceSubnet        string
	instanceAD            string
	instanceCompartment   string
	instanceSshAuthorized string
	vnicDisplayName       string
	vnicHostname          string
	user                  string
	fingerprint           string
	privateKey            string
	tenancy               string
	region                string
	counter               syncfloat64.Counter
	messageRegex          *regexp.Regexp
}

var conf config

func serveMetrics() {
	log.Printf("serving metrics at :2223/metrics")
	http.Handle("/metrics", promhttp.Handler())
	err := http.ListenAndServe(":2223", nil)
	if err != nil {
		log.Fatal(err)
	}
}

func shouldRetry(r common.OCIOperationResponse) bool {
	attrs := []attribute.KeyValue{
		attribute.Key("code").String(strconv.Itoa(r.Response.HTTPResponse().StatusCode)),
	}
	conf.counter.Add(context.TODO(), 1, attrs...)

	msg := conf.messageRegex.FindAllStringSubmatch(r.Error.Error(), 1)
	for i := range msg {
		attrs := []attribute.KeyValue{
			attribute.Key("message").String(msg[i][1]),
		}
		conf.counter.Add(context.TODO(), 1, attrs...)
	}

	if r.Response.HTTPResponse().StatusCode == 429 {
		time.Sleep(5 * time.Second)
	}
	time.Sleep(13 * time.Second)
	return true
}

func main() {
	exporter, err := prometheus.New()
	if err != nil {
		log.Fatal(err)
	}
	provider := metric.NewMeterProvider(metric.WithReader(exporter))
	meter := provider.Meter("goci")

	go serveMetrics()

	ctr, err := meter.SyncFloat64().Counter("oci_requests", instrument.WithDescription("Total number of HTTP requests by type."))
	if err != nil {
		log.Fatal(err)
	}

	conf = config{
		instanceShape:         os.Getenv("INSTANCE_SHAPE"),
		instanceName:          os.Getenv("INSTANCE_NAME"),
		instanceImage:         os.Getenv("INSTANCE_IMAGE"),
		instanceSubnet:        os.Getenv("INSTANCE_SUBNET"),
		instanceAD:            os.Getenv("INSTANCE_AD"),
		instanceCompartment:   os.Getenv("INSTANCE_COMPARTMENT"),
		instanceSshAuthorized: os.Getenv("INSTANCE_SSHAUTHORIZED"),
		vnicDisplayName:       os.Getenv("VNIC_DISPLAY_NAME"),
		vnicHostname:          os.Getenv("VNIC_HOSTNAME"),
		user:                  os.Getenv("USER"),
		fingerprint:           os.Getenv("FINGERPRINT"),
		privateKey:            strings.Replace(os.Getenv("PRIVATE_KEY"),"\\n","\n", -1),
		tenancy:               os.Getenv("TENANCY"),
		region:                os.Getenv("REGION"),
		counter:               ctr,
		messageRegex:          regexp.MustCompile("Message: (.+)\\.?"),
	}

	cfg := common.NewRawConfigurationProvider(conf.tenancy, conf.user, conf.region, conf.fingerprint, conf.privateKey, nil)

	c, err := core.NewComputeClientWithConfigurationProvider(cfg)
	if err != nil {
		log.Fatal(err)
	}

	retryPolicy := common.NewRetryPolicyWithOptions(
		common.WithConditionalOption(true, common.ReplaceWithValuesFromRetryPolicy(common.DefaultRetryPolicyWithoutEventualConsistency())),
		common.WithShouldRetryOperation(shouldRetry),
	)

	request := core.LaunchInstanceRequest{
		LaunchInstanceDetails: core.LaunchInstanceDetails{
			CompartmentId:      common.String(conf.instanceCompartment),
			DisplayName:        common.String(conf.instanceName),
			AvailabilityDomain: common.String(conf.instanceAD),
			InstanceOptions:    &core.InstanceOptions{AreLegacyImdsEndpointsDisabled: common.Bool(false)},
			AvailabilityConfig: &core.LaunchInstanceAvailabilityConfigDetails{
				IsLiveMigrationPreferred: common.Bool(true),
				RecoveryAction:           core.LaunchInstanceAvailabilityConfigDetailsRecoveryActionRestoreInstance,
			},
			CreateVnicDetails: &core.CreateVnicDetails{
				AssignPublicIp: common.Bool(true),
				DisplayName:    common.String(conf.vnicDisplayName),
				HostnameLabel:  common.String(conf.vnicHostname),
				SubnetId:       common.String(conf.instanceSubnet),
			},
			SourceDetails: core.InstanceSourceViaImageDetails{ImageId: common.String(conf.instanceImage)},
			Shape:         common.String(conf.instanceShape),
			ShapeConfig:   &core.LaunchInstanceShapeConfigDetails{Ocpus: common.Float32(4), MemoryInGBs: common.Float32(24)},
			Metadata:      map[string]string{"ssh_authorized_keys": conf.instanceSshAuthorized},
		},
		RequestMetadata: common.RequestMetadata{
			RetryPolicy: &retryPolicy,
		},
	}

	for {
		c.LaunchInstance(context.TODO(), request)
		time.Sleep(13 * time.Second)
	}
}