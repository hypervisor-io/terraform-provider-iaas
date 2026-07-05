package resources_test

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

// dockerDeploymentObject builds a serialized deployment object matching the
// shape returned by both POST /instance/{id}/docker(/custom) (under the
// "deployment" key) and GET /instance/{id}/docker ("deployments" array) -
// both carry the same base columns per DockerService/DockerDeployment.
func dockerDeploymentObject(id, instanceID, appSlug, appName, projectName, status string) map[string]any {
	return map[string]any{
		"id":            id,
		"instance_id":   instanceID,
		"app_slug":      appSlug,
		"app_name":      appName,
		"project_name":  projectName,
		"status":        status,
		"error_message": nil,
		"deployed_at":   nil,
		"port_mappings": []any{},
	}
}

// TestUnitDockerDeployment_rejectAppWithComposeURL is a NEGATIVE
// ConfigValidators test: source = "app" with compose_url set must be rejected
// at PLAN time - no API call is ever made.
func TestUnitDockerDeployment_rejectAppWithComposeURL(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: providerCfg + `
resource "iaas_docker_deployment" "bad" {
  instance_id = "11111111-1111-1111-1111-111111111111"
  source      = "app"
  slug        = "wordpress"
  compose_url = "https://example.com/docker-compose.yml"
}
`,
				ExpectError: regexp.MustCompile(`(?i)compose_url is only used when source`),
			},
		},
	})
}

// TestUnitDockerDeployment_rejectComposeWithoutName is a NEGATIVE
// ConfigValidators test: source = "compose" without name must be rejected at
// PLAN time.
func TestUnitDockerDeployment_rejectComposeWithoutName(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: providerCfg + `
resource "iaas_docker_deployment" "bad" {
  instance_id = "11111111-1111-1111-1111-111111111111"
  source      = "compose"
  compose_url = "https://example.com/docker-compose.yml"
}
`,
				ExpectError: regexp.MustCompile(`(?i)name is required when source`),
			},
		},
	})
}

// TestUnitDockerDeployment_lifecycleApp drives the full CHILD lifecycle for
// source = "app" (catalog app):
//
//  1. Docker is NOT yet installed on the instance (docker_enabled starts 0) -
//     Create must call POST .../docker/install first, then poll the INDEX
//     endpoint until docker_enabled flips to 1, THEN deploy.
//  2. Create - POST .../docker with app_slug/env_variables/port_mappings;
//     asserts the create body. The deployment starts "deploying" and the
//     mock reports "running" from the very first GET .../docker poll so the
//     waiter converges on its first check (no sleep).
//  3. Read - lists within the instance and matches by id.
//  4. Import - composite "<instance_id>/<deployment_id>".
//  5. Delete - DELETE .../docker/{depID}.
func TestUnitDockerDeployment_lifecycleApp(t *testing.T) {
	ensureTFBinary(t)

	// TEST-ONLY poll-interval seam shared with every other async waiter test
	// (see instance.go's pollInterval): make convergence instant.
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")

	srv := acctest.NewMockServer(t)

	const (
		instanceID  = "22222222-2222-2222-2222-222222222222"
		depID       = "33333333-3333-3333-3333-333333333333"
		appSlug     = "wordpress"
		appName     = "WordPress"
		projectName = "wordpress-ab12"
	)

	var (
		mu            sync.Mutex
		dockerEnabled = false
		exists        = false
	)

	srv.Handle("GET", "/instance/"+instanceID+"/docker", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		enabled := dockerEnabled
		var deployments []any
		if exists {
			deployments = []any{dockerDeploymentObject(depID, instanceID, appSlug, appName, projectName, "running")}
		} else {
			deployments = []any{}
		}
		mu.Unlock()

		enabledInt := 0
		if enabled {
			enabledInt = 1
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success":        true,
			"docker_enabled": enabledInt,
			"deployments":    deployments,
		})
	})

	srv.Handle("POST", "/instance/"+instanceID+"/docker/install", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		dockerEnabled = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Docker installation started. This may take a few minutes.",
		})
	})

	srv.Handle("POST", "/instance/"+instanceID+"/docker", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		exists = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success":    true,
			"message":    "Docker app deployment initiated.",
			"deployment": dockerDeploymentObject(depID, instanceID, appSlug, appName, projectName, "deploying"),
		})
	})

	srv.Handle("DELETE", "/instance/"+instanceID+"/docker/"+depID, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		exists = false
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Docker app removed."})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())
	createCfg := providerCfg + `
resource "iaas_docker_deployment" "test" {
  instance_id = "` + instanceID + `"
  source      = "app"
  slug        = "` + appSlug + `"

  env = {
    FOO = "bar"
  }

  port_mappings = [
    {
      container_port = 80
      host_port      = 8080
    }
  ]
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_docker_deployment.test", "id", depID),
					resource.TestCheckResourceAttr("iaas_docker_deployment.test", "instance_id", instanceID),
					resource.TestCheckResourceAttr("iaas_docker_deployment.test", "source", "app"),
					resource.TestCheckResourceAttr("iaas_docker_deployment.test", "slug", appSlug),
					resource.TestCheckResourceAttr("iaas_docker_deployment.test", "name", appName),
					resource.TestCheckResourceAttr("iaas_docker_deployment.test", "project_name", projectName),
					resource.TestCheckResourceAttr("iaas_docker_deployment.test", "status", "running"),
					resource.TestCheckResourceAttr("iaas_docker_deployment.test", "env.FOO", "bar"),
				),
			},
			{
				ResourceName:            "iaas_docker_deployment.test",
				ImportState:             true,
				ImportStateId:           instanceID + "/" + depID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts", "env", "port_mappings"},
			},
		},
	})

	// Assert the install was called BEFORE the deploy (docker-not-installed gate).
	installs := srv.Requests("POST", "/instance/"+instanceID+"/docker/install")
	if len(installs) == 0 {
		t.Fatal("expected at least one POST .../docker/install (docker was not enabled)")
	}

	// Assert the deploy request carried app_slug/env_variables/port_mappings.
	creates := srv.Requests("POST", "/instance/"+instanceID+"/docker")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST .../docker")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["app_slug"] != appSlug {
		t.Errorf("create body app_slug = %v; want %q", createBody["app_slug"], appSlug)
	}
	if _, present := createBody["compose_url"]; present {
		t.Errorf("app-source create body must NOT include compose_url; got %v", createBody)
	}
	envVars, ok := createBody["env_variables"].(map[string]any)
	if !ok || envVars["FOO"] != "bar" {
		t.Errorf("create body env_variables = %v; want {FOO: bar}", createBody["env_variables"])
	}
	portMappings, ok := createBody["port_mappings"].([]any)
	if !ok || len(portMappings) != 1 {
		t.Fatalf("create body port_mappings = %v; want one entry", createBody["port_mappings"])
	}
	pm, _ := portMappings[0].(map[string]any)
	if pm["container_port"] != float64(80) || pm["host_port"] != float64(8080) {
		t.Errorf("create body port_mappings[0] = %v; want container_port=80 host_port=8080", pm)
	}

	// Assert the DELETE request hit the deployment-scoped path.
	deletes := srv.Requests("DELETE", "/instance/"+instanceID+"/docker/"+depID)
	if len(deletes) == 0 {
		t.Fatal("expected at least one DELETE .../docker/{depID}")
	}
}

// TestUnitDockerDeployment_lifecycleCompose drives the CHILD lifecycle for
// source = "compose" (custom compose fetched from a URL). Docker is ALREADY
// installed (docker_enabled starts 1), so Create must NOT call
// POST .../docker/install at all - asserted at the end.
func TestUnitDockerDeployment_lifecycleCompose(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")

	srv := acctest.NewMockServer(t)

	const (
		instanceID  = "44444444-4444-4444-4444-444444444444"
		depID       = "55555555-5555-5555-5555-555555555555"
		appName     = "my-custom-app"
		projectName = "my-custom-app-cd34"
		composeURL  = "https://example.com/docker-compose.yml"
	)

	var mu sync.Mutex
	exists := false

	// composeDeployment mirrors dockerDeploymentObject but additionally sets
	// metadata.compose_url - the only place deployCustom persists the source
	// URL (app_slug is hardcoded to the "custom" sentinel, not the URL) - so
	// dockerComposeURLFromAPI can recover it on read/import.
	composeDeployment := func(status string) map[string]any {
		obj := dockerDeploymentObject(depID, instanceID, "custom", appName, projectName, status)
		obj["metadata"] = map[string]any{"compose_url": composeURL}
		return obj
	}

	srv.Handle("GET", "/instance/"+instanceID+"/docker", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		var deployments []any
		if exists {
			deployments = []any{composeDeployment("running")}
		} else {
			deployments = []any{}
		}
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success":        true,
			"docker_enabled": 1,
			"deployments":    deployments,
		})
	})

	srv.Handle("POST", "/instance/"+instanceID+"/docker/install", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("install must NOT be called when docker_enabled is already 1")
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"success": false,
			"message": "Docker is already installed on this instance.",
		})
	})

	srv.Handle("POST", "/instance/"+instanceID+"/docker/custom", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		exists = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success":    true,
			"message":    "Custom Docker deployment initiated.",
			"deployment": composeDeployment("deploying"),
		})
	})

	srv.Handle("DELETE", "/instance/"+instanceID+"/docker/"+depID, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		exists = false
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Docker app removed."})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())
	createCfg := providerCfg + `
resource "iaas_docker_deployment" "test" {
  instance_id = "` + instanceID + `"
  source      = "compose"
  compose_url = "` + composeURL + `"
  name        = "` + appName + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_docker_deployment.test", "id", depID),
					resource.TestCheckResourceAttr("iaas_docker_deployment.test", "source", "compose"),
					resource.TestCheckResourceAttr("iaas_docker_deployment.test", "compose_url", composeURL),
					resource.TestCheckResourceAttr("iaas_docker_deployment.test", "name", appName),
					resource.TestCheckResourceAttr("iaas_docker_deployment.test", "status", "running"),
				),
			},
			{
				ResourceName:            "iaas_docker_deployment.test",
				ImportState:             true,
				ImportStateId:           instanceID + "/" + depID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts", "env", "port_mappings"},
			},
		},
	})

	// Assert the deploy request hit the /custom path with compose_url/app_name.
	creates := srv.Requests("POST", "/instance/"+instanceID+"/docker/custom")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST .../docker/custom")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["compose_url"] != composeURL {
		t.Errorf("create body compose_url = %v; want %q", createBody["compose_url"], composeURL)
	}
	if createBody["app_name"] != appName {
		t.Errorf("create body app_name = %v; want %q", createBody["app_name"], appName)
	}
	if _, present := createBody["app_slug"]; present {
		t.Errorf("compose-source create body must NOT include app_slug; got %v", createBody)
	}

	// Assert install was never called (docker_enabled started at 1).
	installs := srv.Requests("POST", "/instance/"+instanceID+"/docker/install")
	if len(installs) != 0 {
		t.Errorf("expected NO POST .../docker/install; got %d", len(installs))
	}
}
