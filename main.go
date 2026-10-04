package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/prometheus"
	otelmetric "go.opentelemetry.io/otel/metric"
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
	instanceOcpus         string
	instanceMemory        string
	vnicDisplayName       string
	vnicHostname          string
	user                  string
	fingerprint           string
	privateKey            string
	tenancy               string
	region                string
	telegramToken         string
	telegramChatID        string
	counter               otelmetric.Float64Counter
	gauge                 otelmetric.Float64ObservableGauge
	messageRegex          *regexp.Regexp
	delay                 time.Duration
	lastDelayInc          time.Time
}

var conf config

func serveMetrics() {
	log.Println("serving metrics at :2223/metrics")
	http.Handle("/metrics", promhttp.Handler())
	err := http.ListenAndServe(":2223", nil)
	if err != nil {
		log.Fatal(err)
	}
}

func shouldRetry(r common.OCIOperationResponse) bool {
	response := r.Response.HTTPResponse()

	if response != nil {
		code := response.StatusCode
		message := ""
		attrs := []attribute.KeyValue{
			attribute.Key("code").String(strconv.Itoa(code)),
		}

		msg := conf.messageRegex.FindAllStringSubmatch(r.Error.Error(), 1)
		for i := range msg {
			message = msg[i][1]
			attrs = append(attrs, attribute.Key("message").String(message))
		}

		conf.counter.Add(context.TODO(), 1, otelmetric.WithAttributes(attrs...))
		printMetric(&code, message)

		if code == 429 {
			conf.delay += 1
		} else {
			if time.Now().UTC().Sub(conf.lastDelayInc) > time.Duration(5*time.Minute) {
				conf.delay -= 1
				conf.lastDelayInc = time.Now().UTC()
			}
		}
	} else {
		message := r.Error.Error()
		attrs := []attribute.KeyValue{
			attribute.Key("message").String(message),
		}
		conf.counter.Add(context.TODO(), 1, otelmetric.WithAttributes(attrs...))
		printMetric(nil, message)
	}
	time.Sleep(conf.delay * time.Second)
	return true
}

func printMetric(code *int, message string) {
	entry := struct {
		Metric  string `json:"metric"`
		Code    *int   `json:"code,omitempty"`
		Message string `json:"message,omitempty"`
	}{
		Metric:  "oci_requests",
		Code:    code,
		Message: message,
	}
	out, _ := json.Marshal(entry)
	fmt.Println(string(out))
}

func strValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func telegramEnabled() bool {
	return conf.telegramToken != "" && conf.telegramChatID != ""
}

func notifyTelegram(text string) {
	if !telegramEnabled() {
		return
	}

	form := url.Values{}
	form.Set("chat_id", conf.telegramChatID)
	form.Set("text", text)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.PostForm(fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", conf.telegramToken), form)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		log.Printf("telegram: request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		log.Printf("telegram: unexpected status %d: %s", resp.StatusCode, string(body))
		return
	}

	log.Println("telegram: notification sent")
}

func notifyInstanceLaunched(i core.Instance) {
	if !telegramEnabled() {
		return
	}

	createdAt := ""
	if i.TimeCreated != nil {
		createdAt = i.TimeCreated.Format(time.RFC3339)
	}

	notifyTelegram(fmt.Sprintf(
		"✅ Instância OCI criada\n\nNome: %s\nOCID: %s\nShape: %s\nAD: %s\nRegião: %s\nCriada em: %s",
		strValue(i.DisplayName),
		strValue(i.Id),
		strValue(i.Shape),
		strValue(i.AvailabilityDomain),
		strValue(i.Region),
		createdAt,
	))
}

func main() {
	exporter, err := prometheus.New()
	if err != nil {
		log.Fatal(err)
	}
	provider := metric.NewMeterProvider(metric.WithReader(exporter))
	meter := provider.Meter("goci")

	go serveMetrics()

	ctr, err := meter.Float64Counter("oci_requests", otelmetric.WithDescription("Total number of HTTP requests by type."))
	if err != nil {
		log.Fatal(err)
	}

	gg, err := meter.Float64ObservableGauge("oci_requests_delay", otelmetric.WithDescription("Delay between HTTP requests."))
	if err != nil {
		log.Fatal(err)
	}
	_, err = meter.RegisterCallback(func(ctx context.Context, obs otelmetric.Observer) error {
		obs.ObserveFloat64(gg, float64(conf.delay))
		return nil
	}, gg)
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
		instanceOcpus:         os.Getenv("INSTANCE_OCPUS"),
		instanceMemory:        os.Getenv("INSTANCE_MEMORY_GBS"),
		vnicDisplayName:       os.Getenv("VNIC_DISPLAY_NAME"),
		vnicHostname:          os.Getenv("VNIC_HOSTNAME"),
		user:                  os.Getenv("USER"),
		fingerprint:           os.Getenv("FINGERPRINT"),
		privateKey:            strings.Replace(os.Getenv("PRIVATE_KEY"), "\\n", "\n", -1),
		tenancy:               os.Getenv("TENANCY"),
		region:                os.Getenv("REGION"),
		telegramToken:         os.Getenv("TELEGRAM_TOKEN"),
		telegramChatID:        os.Getenv("TELEGRAM_CHAT_ID"),
		counter:               ctr,
		gauge:                 gg,
		messageRegex:          regexp.MustCompile(`Message: (.+)\.?`),
		delay:                 31,
		lastDelayInc:          time.Now().UTC(),
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

	// parse numeric env vars for shape config
	parseFloat32 := func(s string) float32 {
		if s == "" {
			return 0
		}
		v, err := strconv.ParseFloat(s, 32)
		if err != nil {
			return 0
		}
		return float32(v)
	}

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
			ShapeConfig:   &core.LaunchInstanceShapeConfigDetails{Ocpus: common.Float32(parseFloat32(conf.instanceOcpus)), MemoryInGBs: common.Float32(parseFloat32(conf.instanceMemory))},
			Metadata:      map[string]string{"ssh_authorized_keys": conf.instanceSshAuthorized},
		},
		RequestMetadata: common.RequestMetadata{
			RetryPolicy: &retryPolicy,
		},
	}

	for {
		response, err := c.LaunchInstance(context.TODO(), request)
		if err == nil {
			notifyInstanceLaunched(response.Instance)
		}
		time.Sleep(conf.delay * time.Second)
	}
}
