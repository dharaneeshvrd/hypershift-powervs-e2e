package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/openshift/hypershift/cmd/cluster/core"
	"github.com/openshift/hypershift/cmd/cluster/powervs"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
)

const (
	clusterNameSuffix = "hyp-e2e"
	infraIdSuffix     = "hyp-e2e-infra"
)

type E2eOptions struct {
	ManagementCluster       string              `json:"managementCluster"`
	ManagementClusterRegion string              `json:"managementClusterRegion"`
	PowervsRegion           []string            `json:"powervsRegion"`
	VpcRegion               []string            `json:"vpcRegion"`
	PowervsRegionZoneM      map[string][]string `json:"powervsRegionZoneM"`
	SshKeyPath              string              `json:"sshKeyPath"`
	ResourceGroup           string              `json:"resourceGroup"`
	PullSecret              string              `json:"pullSecret"`
	BaseDomain              string              `json:"baseDomain"`
	NodePoolReplicas        int32               `json:"nodePoolReplicas"`
	ReleaseImage            string              `json:"releaseImage"`
	CpoImage                string              `json:"cpoImage"`
	HypershiftOperatorImage string              `json:"hypershiftOperatorImage"`
}

var apiKey string

func createCluster(options E2eOptions, region string, zone string, vpcRegion string, wg *sync.WaitGroup) {
	defer wg.Done()
	log.Printf("create cluster called with %s region, %s zone and %s vpc region", region, zone, vpcRegion)
	ctx := context.Background()

	coreCreateOpt := core.CreateOptions{
		Namespace:                      "clusters",
		ControlPlaneAvailabilityPolicy: "SingleReplica",
		Render:                         false,
		InfrastructureJSON:             "",
		ServiceCIDR:                    "172.31.0.0/16",
		PodCIDR:                        "10.132.0.0/14",
		Wait:                           false,
		Timeout:                        0,
		ExternalDNSDomain:              "",
		AdditionalTrustBundle:          "",
		ImageContentSources:            "",
	}

	coreCreateOpt.PowerVSPlatform = core.PowerVSPlatformOptions{
		APIKey:        os.Getenv("IBMCLOUD_API_KEY"),
		Region:        region,
		Zone:          zone,
		VpcRegion:     vpcRegion,
		ResourceGroup: options.ResourceGroup,
		SysType:       "s922",
		ProcType:      "shared",
		Processors:    "0.5",
		Memory:        32,
	}

	coreCreateOpt.Name = fmt.Sprintf("%s-%s", zone, clusterNameSuffix)
	coreCreateOpt.InfraID = fmt.Sprintf("%s-%s", zone, infraIdSuffix)
	coreCreateOpt.SSHKeyFile = options.SshKeyPath
	coreCreateOpt.PullSecretFile = options.PullSecret
	coreCreateOpt.BaseDomain = options.BaseDomain
	coreCreateOpt.NodePoolReplicas = options.NodePoolReplicas
	coreCreateOpt.ReleaseImage = options.ReleaseImage
	coreCreateOpt.ControlPlaneOperatorImage = options.CpoImage
	log.Printf("core opt: %+v", coreCreateOpt)
	err := powervs.CreateCluster(ctx, &coreCreateOpt)
	if err != nil {
		log.Printf("error create cluster %s %v", coreCreateOpt.Name, err)
		return
	}

	log.Printf("create cluster completed %s", coreCreateOpt.Name)
}

func rune2e(options E2eOptions) {
	var wg sync.WaitGroup

	for index, region := range options.PowervsRegion {
		for _, zone := range options.PowervsRegionZoneM[region] {
			vpcRegion := options.VpcRegion[index]
			go createCluster(options, region, zone, vpcRegion, &wg)
			wg.Add(1)
		}
	}

	wg.Wait()
}

func setupEnv(clusterRegion string, clusterName string, hypershiftImage string) error {
	loginArgs := []string{"login", fmt.Sprintf("--apikey=%s", apiKey), "-r", clusterRegion}
	cmd := exec.Command("ibmcloud", loginArgs...)
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("error running ibmcloud login %v", err)
	}

	osPluginInstallArgs := []string{"plugin", "install", "container-service"}
	cmd = exec.Command("ibmcloud", osPluginInstallArgs...)
	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("error running plugin install %v", err)
	}

	getClusterDetailsArgs := []string{"oc", "cluster", "get", "-c", clusterName, "--output", "json"}
	cmd = exec.Command("ibmcloud", getClusterDetailsArgs...)
	result, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("error running get cluster %v", err)
	}

	var cluster map[string]interface{}
	err = json.Unmarshal(result, &cluster)
	if err != nil {
		return fmt.Errorf("error parsing get cluster output %v", err)
	}

	masterUrl := cluster["masterURL"].(string)

	oauthUrl := fmt.Sprintf("%s/.well-known/oauth-authorization-server", masterUrl)

	resp, err := http.Get(oauthUrl)
	if err != nil {
		return fmt.Errorf("error calling %s %v", oauthUrl, err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error reading resp body for %s %v", oauthUrl, err)
	}

	var oauthDetails map[string]interface{}
	err = json.Unmarshal(body, &oauthDetails)
	if err != nil {
		return fmt.Errorf("error parsing get cluster output %v", err)
	}

	tokenEP := oauthDetails["token_endpoint"].(string)
	tokenEPUrl, err := url.Parse(tokenEP)
	if err != nil {
		return fmt.Errorf("error parsing token endpoint url %s %v", tokenEP, err)
	}
	oauthAuthorizeUrl := fmt.Sprintf("%s://%s/oauth/authorize?client_id=openshift-challenging-client&response_type=token", tokenEPUrl.Scheme, tokenEPUrl.Host)

	req, err := http.NewRequest("GET", oauthAuthorizeUrl, nil)
	if err != nil {
		return fmt.Errorf("error creating request for %s %v", oauthAuthorizeUrl, err)
	}

	req.Header.Add("X-CSRF-Token", "a")
	req.SetBasicAuth("apikey", apiKey)

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("error calling %s %v", oauthAuthorizeUrl, err)
	}

	var accessToken string
	locationUrl := resp.Request.URL
	if err != nil {
		return fmt.Errorf("error parsing location url %v", err)
	}
	locationUrlS := strings.Split(locationUrl.Fragment, "&")
	for _, frag := range locationUrlS {
		if strings.Contains(frag, "access_token") {
			accessTokenSpl := strings.Split(frag, "=")
			accessToken = accessTokenSpl[1]
		}
	}
	log.Printf("accessToken: %s", accessToken)

	ocLoginArgs := []string{"login", fmt.Sprintf("--token=%s", accessToken), fmt.Sprintf("--server=%s", masterUrl)}
	cmd = exec.Command("oc", ocLoginArgs...)
	log.Printf("oc login cmd: %v", cmd.String())
	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("error running oc login %v", err)
	}

	installHypershitPrereq := []string{"install", "--hypershift-image", hypershiftImage}
	cmd = exec.Command("./hypershift-main/bin/hypershift", installHypershitPrereq...)
	log.Println(cmd.String())
	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("error running hypershift install %v", err)
	}

	return nil
}

func main() {
	args := os.Args[1:]

	if len(args) <= 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Println("Usage: ./hypershift-powervs-e2e <configFile>")
		return
	}

	configFile := args[0]
	log.Println(configFile)

	rawConfig, err := ioutil.ReadFile(configFile)
	if err != nil {
		log.Printf("failed to read config json file: %w", err)
	}
	log.Printf("%s", rawConfig)
	var options E2eOptions
	if err = json.Unmarshal(rawConfig, &options); err != nil {
		log.Printf("failed to unmarshal config json: %w", err)
	}
	log.Printf("options: %+v", options)

	apiKey = os.Getenv("IBMCLOUD_API_KEY")

	err = setupEnv(options.ManagementClusterRegion, options.ManagementCluster, options.HypershiftOperatorImage)
	if err != nil {
		log.Printf("error setup env %w", err)
		return
	}

	rune2e(options)
	return
}

/*
curl https://raw.githubusercontent.com/canha/golang-tools-install-script/master/goinstall.sh | bash -s -- --version 1.18
curl -fsSL https://clis.cloud.ibm.com/install/linux | sh
ibmcloud plugin install container-service
ibmcloud login --apikey=<api_key> -r jp-tok
ibmcloud ks cluster config --cluster dhar-hyp-ocp
*/

/*
ibmcloud oc cluster get -c dhar-hyp-ocp
get master url
curl https://c115-e.jp-tok.containers.cloud.ibm.com:32385/.well-known/oauth-authorization-server
get issuer
curl -u 'apikey:<api_key>' -H "X-CSRF-Token: a" 'https://c115-e.jp-tok.containers.cloud.ibm.com:30181/oauth/authorize?client_id=openshift-challenging-client&response_type=token' -v
get access token from location
oc login --token=<access_token> --server=https://c115-e.jp-tok.containers.cloud.ibm.com:32385
*/
