packer {
  required_plugins {
    googlecompute = {
      version = ">= 1.1.6"
      source = "github.com/hashicorp/googlecompute"
    }
  }
}

variable "project_id" {
  type = string
}

variable "region" {
  type = string
}

variable "zone" {
  type = string
}

variable "builder_sa" {
  type = string
}

variable "buildkite_apikey" {
  type = string
}

source "googlecompute" "buildkite-agent" {
  image_name                  = "buildkite-agent"
  project_id                  = var.project_id
  source_image_family         = "debian-12"
  zone                        = var.zone
  image_description           = "Created with HashiCorp Packer from Cloudbuild"
  ssh_username                = "packer"
  tags                        = ["packer"]
  network                     = join("", ["projects/", var.project_id, "/global/networks/default"])
  subnetwork                  = join("", ["projects/", var.project_id, "/regions/", var.region, "/subnetworks/default"])
}

build {
  sources = ["sources.googlecompute.buildkite-agent"]

  provisioner "file" {
    source = "ops-agent.service"
    destination = "/tmp/ops-agent.service"
  }
  provisioner "shell" {
    inline = [
      "sudo mv /tmp/ops-agent.service /etc/systemd/system/ops-agent.service",
      "sudo systemctl enable ops-agent.service",
    ]
  }
  # Install dependencies for the build.
  # TODO(austin): Shrink this list by making the build more hermetic...
  provisioner "shell" {
    inline = [
      "sudo apt-get install -y libgl1 locales libunwind8 libnss3 libatk1.0-0 libatk-bridge2.0-0 libcups2 libgtk-3-0 libgbm1 libasound2 xvfb",
      "sudo localedef -i en_US -f UTF-8 en_US.UTF-8",
    ]
  }
  # Use a local NGINX proxy to make things faster.
  provisioner "shell" {
    inline = [
      "echo '127.0.0.1 realtimeroboticsgroup.org' | sudo tee -a /etc/hosts",
      "echo '10.138.0.4 public.realtimeroboticsgroup.org' | sudo tee -a /etc/hosts",
    ]
  }
  provisioner "file" {
    source = "make-self-signed-certificates.sh"
    destination = "/tmp/make-self-signed-certificates.sh"
  }
  provisioner "shell" {
    inline = [
      "sudo apt-get install -y nginx",
      # Install the tools to build the system java keystore.
      "sudo apt-get install -y openjdk-17-jre",
      "sudo chmod 755 /tmp/make-self-signed-certificates.sh",
      "sudo /tmp/make-self-signed-certificates.sh /etc/nginx/selfsigned",
      "rm /tmp/make-self-signed-certificates.sh",
      # And now remove them, the keystore will continue to exist
      "sudo dpkg --purge openjdk-17-jre",
      "sudo apt-get autoremove -y",
      # Make sure it survived.
      "test -e /etc/ssl/certs/java/cacerts",
    ]
  }
  provisioner "file" {
    source = "realtimeroboticsgroup_proxy_site"
    destination = "/tmp/realtimeroboticsgroup_proxy_site"
  }
  provisioner "shell" {
    inline = [
      "sudo mv /tmp/realtimeroboticsgroup_proxy_site /etc/nginx/sites-enabled/",
      "sudo chown root:root /etc/nginx/sites-enabled/realtimeroboticsgroup_proxy_site",
      "sudo chmod 400 /etc/nginx/sites-enabled/realtimeroboticsgroup_proxy_site",
    ]
  }
  provisioner "file" {
    source = "build-dependencies.service"
    destination = "/tmp/build-dependencies.service"
  }
  provisioner "file" {
    source = "gcsproxy"
    destination = "/tmp/gcsproxy"
  }
  provisioner "shell" {
    inline = [
      "sudo mv /tmp/build-dependencies.service /etc/systemd/system/build-dependencies.service",
      "sudo chown root:root /etc/systemd/system/build-dependencies.service",
      "sudo chmod 644 /etc/systemd/system/build-dependencies.service",
      "sudo mv /tmp/gcsproxy /usr/local/bin/gcsproxy",
      "sudo chown root:root /usr/local/bin/gcsproxy",
      "sudo chmod 755 /usr/local/bin/gcsproxy",
      "sudo systemctl enable build-dependencies.service",
    ]
  }

  provisioner "file" {
    source = "gerrit-ssh-proxy.service"
    destination = "/tmp/gerrit-ssh-proxy.service"
  }
  provisioner "shell" {
    inline = [
      "sudo apt-get install socat",
      "sudo mv /tmp/gerrit-ssh-proxy.service /etc/systemd/system/gerrit-ssh-proxy.service",
      "sudo chown root:root /etc/systemd/system/gerrit-ssh-proxy.service",
      "sudo chmod 644 /etc/systemd/system/gerrit-ssh-proxy.service",
      "sudo systemctl enable gerrit-ssh-proxy.service",
    ]
  }

  provisioner "file" {
    source = "10-power-off-stop.conf"
    destination = "/tmp/10-power-off-stop.conf"
  }
  provisioner "shell" {
    inline = [
      "sudo mkdir /etc/systemd/system/buildkite-agent.service.d/",
      "sudo mv /tmp/10-power-off-stop.conf /etc/systemd/system/buildkite-agent.service.d/10-power-off-stop.conf",
      "sudo chown root:root /etc/systemd/system/buildkite-agent.service.d/10-power-off-stop.conf",
    ]
  }
  provisioner "file" {
    source = "terminate-instance"
    destination = "/tmp/terminate-instance"
  }
  provisioner "shell" {
    inline = [
      "sudo mv /tmp/terminate-instance /usr/local/bin/",
      "sudo chown root:root /usr/local/bin/terminate-instance",
      "sudo chmod 555 /usr/local/bin/terminate-instance",
    ]
  }
  provisioner "shell" {
    inline = [
      "curl -fsSL https://keys.openpgp.org/vks/v1/by-fingerprint/32A37959C2FA5C3C99EFBC32A79206696452D198 | sudo gpg --dearmor -o /usr/share/keyrings/buildkite-agent-archive-keyring.gpg",
      "echo 'deb [signed-by=/usr/share/keyrings/buildkite-agent-archive-keyring.gpg] https://apt.buildkite.com/buildkite-agent stable main' | sudo tee /etc/apt/sources.list.d/buildkite-agent.list",
      "sudo apt-get update && sudo apt-get install -y buildkite-agent",
      join("", ["sudo sed -i 's/xxx/", var.buildkite_apikey, "/g' /etc/buildkite-agent/buildkite-agent.cfg"]),
      "sudo sed -i 's/^# tags-from-gcp=true/tags-from-gcp=true/g' /etc/buildkite-agent/buildkite-agent.cfg",
      "echo '# Shutdown when there are no jobs' | sudo tee -a /etc/buildkite-agent/buildkite-agent.cfg",
      "echo 'disconnect-after-idle-timeout=300' | sudo tee -a /etc/buildkite-agent/buildkite-agent.cfg",
      # Wait for the network to come online so we have a hostname, and nginx to come up so the proxy works.
      "sudo sed -i 's/After=network.target/After=network-online.target nginx.service build-dependencies.service/g' /lib/systemd/system/buildkite-agent.service",
      "sudo systemctl enable buildkite-agent",
    ]
  }
  provisioner "file" {
    source = "id_rsa"
    destination = "/tmp/id_rsa"
  }
  provisioner "shell" {
    inline = [
      "sudo mkdir /var/lib/buildkite-agent/.ssh/",
      "sudo chown buildkite-agent:buildkite-agent /var/lib/buildkite-agent/.ssh/",
      "sudo chmod 700 /var/lib/buildkite-agent/.ssh/",
      "sudo mv /tmp/id_rsa /var/lib/buildkite-agent/.ssh/",
      "sudo chown buildkite-agent:buildkite-agent /var/lib/buildkite-agent/.ssh/id_rsa",
      "sudo chmod 400 /var/lib/buildkite-agent/.ssh/id_rsa",
    ]
  }
  provisioner "file" {
    source = "buildkite-agent.bazelrc"
    destination = "/tmp/buildkite-agent.bazelrc"
  }
  provisioner "shell" {
    inline = [
      "sudo mv /tmp/buildkite-agent.bazelrc ~buildkite-agent/.bazelrc",
      "sudo chown buildkite-agent:buildkite-agent ~buildkite-agent/.bazelrc",
      "sudo chmod 444 ~buildkite-agent/.bazelrc",
    ]
  }

  provisioner "shell" {
    inline = [
      "sudo apt-get clean",
    ]
  }
}
