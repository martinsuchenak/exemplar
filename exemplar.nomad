# exemplar on Nomad, with the Presidio Analyzer it anonymises with. See
# docs/deployment.md.
#
#   nomad var put nomad/jobs/exemplar anon_key=$(openssl rand -hex 32)   # optional
#   nomad job run -var image=registry.example.com/exemplar:1.0.0 exemplar.nomad
#
# Needs a client with the Docker driver, CNI plugins (bridge networking) and a
# host volume called exemplar-data, writable by UID 10001:
#
#   client {
#     host_volume "exemplar-data" {
#       path      = "/opt/exemplar/data"
#       read_only = false
#     }
#   }

variable "image" {
  type        = string
  description = "The exemplar image, built from the Dockerfile and pushed where the clients can pull it"
  default     = "exemplar:latest"
}

variable "presidio_image" {
  type        = string
  description = "The Presidio Analyzer image; pin a version you have tested"
  default     = "mcr.microsoft.com/presidio-analyzer:latest"
}

variable "datacenters" {
  type    = list(string)
  default = ["dc1"]
}

job "exemplar" {
  datacenters = var.datacenters
  type        = "service"

  group "exemplar" {
    # SQLite allows one server per database: never more than one.
    count = 1

    network {
      # The tasks share it, so exemplar reaches Presidio on localhost.
      mode = "bridge"
      port "http" {
        to = 8080
      }
    }

    volume "data" {
      type      = "host"
      source    = "exemplar-data"
      read_only = false
    }

    service {
      name     = "exemplar"
      port     = "http"
      provider = "nomad"

      check {
        type     = "http"
        path     = "/health"
        interval = "10s"
        timeout  = "2s"
      }
    }

    # Presidio starts first and runs alongside. It is not published: only
    # exemplar can reach it.
    task "presidio" {
      driver = "docker"

      lifecycle {
        hook    = "prestart"
        sidecar = true
      }

      config {
        image = var.presidio_image
      }

      env {
        # The default 30s worker timeout kills the worker while the model loads.
        GUNICORN_CMD_ARGS = "--timeout 300"
      }

      resources {
        cpu    = 1000
        memory = 2048
      }
    }

    task "exemplar" {
      driver = "docker"

      config {
        image = var.image
        ports = ["http"]
      }

      volume_mount {
        volume      = "data"
        destination = "/app/data"
      }

      env {
        PRESIDIO_URL = "http://localhost:3000"
      }

      # The anonymisation key, from the Nomad variable nomad/jobs/exemplar
      # (item anon_key). Without the variable, ingest of real data stays off
      # and the job still runs.
      template {
        data        = <<-EOT
          {{- range nomadVarList "nomad/jobs/exemplar" }}{{ if eq .Path "nomad/jobs/exemplar" }}
          {{- with nomadVar .Path }}EXEMPLAR_ANON_KEY={{ .anon_key }}{{ end }}
          {{- end }}{{ end }}
        EOT
        destination = "secrets/anon.env"
        env         = true
        change_mode = "restart"
      }

      # serve lets requests in flight finish for up to 30 seconds.
      kill_timeout = "35s"

      resources {
        cpu    = 256
        memory = 256
      }
    }
  }
}
