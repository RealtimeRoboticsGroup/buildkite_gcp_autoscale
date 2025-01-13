package main

import (
	"context"
	"slices"

	"flag"
	"fmt"
	"log"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	computepb "cloud.google.com/go/compute/apiv1/computepb"

	"google.golang.org/protobuf/proto"

	"github.com/buildkite/buildkite-agent-scaler/buildkite"
	"google.golang.org/api/iterator"
)

type State struct {
	// Agent API token expected to service requests
	AgentToken string
	// BuildkiteProject in gerrit to only accept events from.
	BuildkiteProject string
	// Organization to use in Buildkite for the build.
	BuildkiteOrganization string
	// Buildkite queue
	Queue string

	// GCP project ID
	ProjectID string
	// GCP region
	Region string
	// GCP Zone
	Zone string

	// GCP machine type to start up.
	MachineType string

	// Service account to start the worker as.
	ServiceAccount string

	// GCP Image to run on that machine
	ImageName string

	// Buildkite client.
	client *buildkite.Client

	// GCP instances client
	instancesClient *compute.InstancesClient
	ctx             context.Context

	lastInstanceNumber int64
}

func (s *State) OpenGCPClient() error {
	s.ctx = context.Background()
	instancesClient, err := compute.NewInstancesRESTClient(s.ctx)
	s.instancesClient = instancesClient
	return err
}

func (s *State) ListInstances(zone string) *compute.InstanceIterator {
	req := &computepb.ListInstancesRequest{
		Project: s.ProjectID,
		Zone:    zone,
	}

	return s.instancesClient.List(s.ctx, req)
}

func (s *State) CountInstancesWithTag(zones []string, tag string) int64 {
	var count int64 = 0
	for _, zone := range zones {
		it := s.ListInstances(zone)
		for {
			resp, err := it.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				log.Fatalf("Failed to get next: %s\n", err)
			}

			log.Printf("Response: %s, %s, %s\n", *resp.Name, resp.GetTags().GetItems(), *resp.Status)
			if slices.Contains(resp.GetTags().GetItems(), tag) {
				count += 1
			}
		}
	}
	return count
}

func PrintInstances(s *State, zones []string) {
	for _, zone := range zones {
		it := s.ListInstances(zone)
		for {
			resp, err := it.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				log.Fatalf("Failed to get next: %s\n", err)
			}

			log.Printf("Response: %s, %s, %s\n", *resp.Name, resp.GetTags().GetItems(), *resp.Status)
			if slices.Contains(resp.GetTags().GetItems(), "buildkite-agent") {
				log.Printf("Agent!")
			}
		}
	}
}

func (s *State) StartInstance(name string) (*compute.Operation, error) {
	req := &computepb.InsertInstanceRequest{
		Project: s.ProjectID,
		Zone:    s.Zone,
		InstanceResource: &computepb.Instance{
			Name: proto.String(name),
			Disks: []*computepb.AttachedDisk{
				{
					InitializeParams: &computepb.AttachedDiskInitializeParams{
						DiskSizeGb:  proto.Int64(300),
						SourceImage: proto.String(fmt.Sprintf("projects/%s/global/images/%s", s.ProjectID, s.ImageName)),
						DiskType:    proto.String(fmt.Sprintf("zones/%s/diskTypes/pd-ssd", s.Zone)),
					},
					AutoDelete: proto.Bool(true),
					Boot:       proto.Bool(true),
					Type:       proto.String(computepb.AttachedDisk_PERSISTENT.String()),
				},
			},
			MachineType: proto.String(fmt.Sprintf("zones/%s/machineTypes/%s", s.Zone, s.MachineType)),
			NetworkInterfaces: []*computepb.NetworkInterface{
				{
					Name: proto.String("global/networks/default"),
					AccessConfigs: []*computepb.AccessConfig{
						{
							Name:        proto.String("External NAT"),
							NetworkTier: proto.String(computepb.AccessConfig_STANDARD.String()),
						},
					},
					StackType:  proto.String(computepb.BackendService_IPV4_ONLY.String()),
					Subnetwork: proto.String(fmt.Sprintf("projects/%s/regions/%s/subnetworks/ipv6", s.ProjectID, s.Region)),
				},
			},
			// Make it a spot instance which stays up for at most 8 hours.
			Scheduling: &computepb.Scheduling{
				AutomaticRestart:          proto.Bool(false),
				InstanceTerminationAction: proto.String(computepb.Scheduling_DELETE.String()),
				OnHostMaintenance:         proto.String("TERMINATE"),
				MaxRunDuration: &computepb.Duration{
					Seconds: proto.Int64(8 * 3600),
				},
				ProvisioningModel: proto.String(computepb.Scheduling_SPOT.String()),
			},
			ServiceAccounts: []*computepb.ServiceAccount{
				{
					Email: proto.String(s.ServiceAccount),
					Scopes: []string{
						"https://www.googleapis.com/auth/logging.write",
						"https://www.googleapis.com/auth/monitoring.write",
						"https://www.googleapis.com/auth/compute",
						"https://www.googleapis.com/auth/devstorage.read_write",
					},
				},
			},
			Metadata: &computepb.Metadata{
				Items: []*computepb.Items{
					{
						Key:   proto.String("buildkite-idle-shutdown-time"),
						Value: proto.String("300"),
					},
				},
			},
			Tags: &computepb.Tags{
				Items: []string{"ssh", "icmp", "buildkite-agent"},
			},
		},
	}

	return s.instancesClient.Insert(s.ctx, req)
}

func (s *State) OpenClient() {
	s.client = buildkite.NewClient(s.AgentToken)
}

func (s *State) GetAgentMetrics() (buildkite.AgentMetrics, error) {
	return s.client.GetAgentMetrics(s.Queue)
}

func (s *State) InstanceNumber() int64 {
	result := max(s.lastInstanceNumber+1, time.Now().Unix())
	s.lastInstanceNumber = result
	return result
}

// TODO(austin): Lock down service user permissions for buildkite so it can only delete.

func main() {
	agentToken := flag.String("agent_token", "", "Agent API token")
	queue := flag.String("queue", "", "Queue to autoscale")
	serviceAccount := flag.String("service-account", "", "Service account to run the build as")
	buildkiteProject := flag.String("buildkite_project", "ci", "Buildkite project to monitor")
	buildkiteOrganization := flag.String("organization", "realtimeroboticsgroup", "Project to filter events for")
	projectID := flag.String("project_id", "", "GCP Project ID")
	region := flag.String("region", "us-west1", "GCP region")
	zoneLetter := flag.String("zone", "b", "GCP zone")
	machineType := flag.String("machine_type", "c3-standard-4", "GCP instance type")
	imageName := flag.String("image_name", "buildkite-agent", "GCP image name")
	maxInstances := flag.Int64("max_instances", 4, "Max containers to start")

	flag.Parse()

	state := State{
		AgentToken:            *agentToken,
		BuildkiteProject:      *buildkiteProject,
		BuildkiteOrganization: *buildkiteOrganization,
		Queue:                 *queue,
		ProjectID:             *projectID,
		Region:                *region,
		Zone:                  fmt.Sprintf("%s-%s", *region, *zoneLetter),
		MachineType:           *machineType,
		ServiceAccount:        *serviceAccount,
		ImageName:             *imageName,
		lastInstanceNumber:    0,
	}

	state.OpenClient()
	if err := state.OpenGCPClient(); err != nil {
		// TODO: Handle error better.  Retry?  Or just let systemd do it?
		log.Fatalf("Failed to open client %v\n", err)
	}
	log.Printf("Client: %s\n", state.client)

	defer state.instancesClient.Close()

	for {
		metrics, err := state.GetAgentMetrics()
		if err != nil {
			log.Printf("Failed to get agent metrics: %v\n", err)
			time.Sleep(100 * time.Second)
			continue
		}
		log.Printf("Metrics: {\"OrgSlug\": \"%s\", \"Queue\": \"%s\", \"ScheduledJobs\": %d, \"RunningJobs\": %d, \"PollDuration\": %s, \"WaitingJobs\": %d, \"IdleAgents\": %d, \"BusyAgents\": %d, \"TotalAgents\": %d}\n", metrics.OrgSlug, metrics.Queue, metrics.ScheduledJobs, metrics.RunningJobs, metrics.PollDuration, metrics.WaitingJobs, metrics.IdleAgents, metrics.BusyAgents, metrics.TotalAgents)

		zones := []string{"us-west1-a", "us-west1-b", "us-west1-c"}

		desiredWorkers := metrics.RunningJobs + metrics.ScheduledJobs + metrics.WaitingJobs

		// Not even worth the effort to see how many are running.  If buildkite is happy, don't even bother polling GCP to see if we need to start more.
		if desiredWorkers <= metrics.TotalAgents {
			time.Sleep(metrics.PollDuration)
			continue
		}

		instanceCount := state.CountInstancesWithTag(zones, "buildkite-agent")

		if desiredWorkers > instanceCount && instanceCount < *maxInstances {
			log.Printf("Not enough workers, need %d, have %d, starting one\n", desiredWorkers, instanceCount)

			instanceName := fmt.Sprintf("buildkite-agent-%d", state.InstanceNumber())
			op, err := state.StartInstance(instanceName)

			if err != nil {
				log.Fatal(fmt.Errorf("unable to create instance: %w", err))
			}

			// TODO(austin): This seems to catch failure to start instance, seems worth keeping around.  Not sure fatal is the right response though.
			if err = op.Wait(state.ctx); err != nil {
				log.Fatalf("unable to wait for the operation: %w", err)
			}
		}
		time.Sleep(metrics.PollDuration)
	}
}
