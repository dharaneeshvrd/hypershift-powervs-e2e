package main

import (
	"encoding/json"
	"fmt"
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
	clusterNameSuffix       = "hyp-e2e"
	infraIdSuffix           = "hyp-e2e-infra"
	managementCluster       = "dhar-hyp-ocp"
	managementClusterRegion = "jp-tok"
	resourceGroup           = "ibm-hypershift-dev"
	baseDomain              = "hypershift-ppc64le.com"
	nodePoolReplicas        = "2"
	releaseImagePath        = "quay.io/openshift-release-dev/ocp-release"
)

var (
	powervsRegion      = []string{"osa"}
	vpcRegion          = []string{"jp-osa"}
	powervsRegionZoneM = map[string][]string{"osa": {"osa21"}}
)

type E2eOptions struct {
	SshKeyPath string `json:"sshKeyPath"`
	PullSecret string `json:"pullSecret"`
}

var apiKey string

func createCluster(options E2eOptions, region string, zone string, vpcRegion string, wg *sync.WaitGroup) {
	defer wg.Done()
	log.Printf("create cluster called with %s region, %s zone and %s vpc region", region, zone, vpcRegion)

	name := fmt.Sprintf("%s-%s", zone, clusterNameSuffix)
	infraID := fmt.Sprintf("%s-%s", zone, infraIdSuffix)
	sshKeyFile := options.SshKeyPath
	pullSecretFile := options.PullSecret

	resp, err := http.Get("https://multi.ocp.releases.ci.openshift.org/graph")
	if err != nil {
		log.Printf("error retrieving release image %w", err)
		return
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("error reading resp body for ocp nightly multi arch %w", err)
		return
	}

	var releaseImages map[string]interface{}
	err = json.Unmarshal(body, &releaseImages)
	if err != nil {
		log.Printf("error parsing get cluster output %w", err)
		return
	}
	imageList := releaseImages["nodes"].([]interface{})
	latestImage := imageList[0].(map[string]string)

	releaseImage := fmt.Sprintf("%s:%s", releaseImagePath, latestImage["version"])

	hypershiftCreateClusterArgs := []string{
		"create", "cluster", "powervs",
		"--region", region,
		"--zone", zone,
		"--vpc-region", vpcRegion,
		"--name", name,
		"--infra-id", infraID,
		"--resource-group", resourceGroup,
		"--base-domain", baseDomain,
		"--pull-secret", pullSecretFile,
		"--ssh-key", sshKeyFile,
		"--release-image", releaseImage,
		"--node-pool-replicas", nodePoolReplicas,
	}

	cmd := exec.Command("./hypershift-main/bin/hypershift", hypershiftCreateClusterArgs...)
	log.Printf("create cluster command %v", cmd.String())
	err = cmd.Run()

	if err != nil {
		log.Printf("error create cluster %s %w", name, err)
		return
	}

	log.Printf("create cluster completed %s", name)
}

func rune2e(options E2eOptions) {
	var wg sync.WaitGroup

	for index, region := range powervsRegion {
		for _, zone := range powervsRegionZoneM[region] {
			vpcRegion := vpcRegion[index]
			go createCluster(options, region, zone, vpcRegion, &wg)
			wg.Add(1)
		}
	}

	wg.Wait()
}

func setupEnv(clusterRegion string, clusterName string) error {
	loginArgs := []string{"login", fmt.Sprintf("--apikey=%s", apiKey), "-r", clusterRegion}
	cmd := exec.Command("ibmcloud", loginArgs...)
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("error running ibmcloud login %w", err)
	}

	osPluginInstallArgs := []string{"plugin", "install", "container-service"}
	cmd = exec.Command("ibmcloud", osPluginInstallArgs...)
	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("error running plugin install %w", err)
	}

	getClusterDetailsArgs := []string{"oc", "cluster", "get", "-c", clusterName, "--output", "json"}
	cmd = exec.Command("ibmcloud", getClusterDetailsArgs...)
	result, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("error running get cluster %w", err)
	}

	var cluster map[string]interface{}
	err = json.Unmarshal(result, &cluster)
	if err != nil {
		return fmt.Errorf("error parsing get cluster output %w", err)
	}

	masterUrl := cluster["masterURL"].(string)

	oauthUrl := fmt.Sprintf("%s/.well-known/oauth-authorization-server", masterUrl)

	resp, err := http.Get(oauthUrl)
	if err != nil {
		return fmt.Errorf("error calling %s %w", oauthUrl, err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error reading resp body for %s %w", oauthUrl, err)
	}

	var oauthDetails map[string]interface{}
	err = json.Unmarshal(body, &oauthDetails)
	if err != nil {
		return fmt.Errorf("error parsing get cluster output %w", err)
	}

	tokenEP := oauthDetails["token_endpoint"].(string)
	tokenEPUrl, err := url.Parse(tokenEP)
	if err != nil {
		return fmt.Errorf("error parsing token endpoint url %s %w", tokenEP, err)
	}
	oauthAuthorizeUrl := fmt.Sprintf("%s://%s/oauth/authorize?client_id=openshift-challenging-client&response_type=token", tokenEPUrl.Scheme, tokenEPUrl.Host)

	req, err := http.NewRequest("GET", oauthAuthorizeUrl, nil)
	if err != nil {
		return fmt.Errorf("error creating request for %s %w", oauthAuthorizeUrl, err)
	}

	req.Header.Add("X-CSRF-Token", "a")
	req.SetBasicAuth("apikey", apiKey)

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("error calling %s %w", oauthAuthorizeUrl, err)
	}

	var accessToken string
	locationUrl := resp.Request.URL
	if err != nil {
		return fmt.Errorf("error parsing location url %w", err)
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
		return fmt.Errorf("error running oc login %w", err)
	}

	installHypershitPrereq := []string{"install"}
	cmd = exec.Command("./hypershift-main/bin/hypershift", installHypershitPrereq...)
	log.Println(cmd.String())
	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("error running hypershift install %w", err)
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

	err = setupEnv(managementClusterRegion, managementCluster)
	if err != nil {
		log.Printf("error setup env %w", err)
		return
	}

	rune2e(options)
	return
}
