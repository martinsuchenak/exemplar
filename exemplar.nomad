# exemplar on Nomad, with the Presidio Analyzer it anonymises with. See
# docs/deployment.md.
#
#   nomad var put nomad/jobs/exemplar anon_key=$(openssl rand -hex 32)   # optional
#   nomad job run -var image=ghcr.io/martinsuchenak/exemplar:0.5.0 exemplar.nomad
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
  description = "The exemplar image: a release from ghcr.io (pin a version in production), or one built from the Dockerfile and pushed where the clients can pull it"
  default     = "ghcr.io/martinsuchenak/exemplar:latest"
}

variable "presidio_image" {
  type        = string
  description = "The Presidio Analyzer image: the version exemplar is tested against, or another you have tested"
  default     = "mcr.microsoft.com/presidio-analyzer:2.2.362"
}

variable "runs" {
  type        = number
  description = "Ingest and anonymise runs exemplar holds at once. Presidio gets one worker for each, and both tasks' memory follows: about 1 GiB per run for exemplar (uploads of up to 100 MiB) and 768 MiB per worker for Presidio"
  default     = 4

  validation {
    condition     = var.runs >= 1 && var.runs <= 16
    error_message = "runs must be from 1 to 16."
  }
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
        # One worker answers one request at a time, and each loads its own
        # model, so memory grows with them.
        WORKERS = var.runs
      }

      resources {
        cpu    = 500 + 500 * var.runs
        memory = 1280 + 768 * var.runs
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
        # An ingest or anonymise run holds its upload in memory and parsing
        # takes several times more, so these and the memory below go
        # together: a run of up to 100 MiB fits in 1 GiB.
        MAX_INGEST_MB       = "100"
        MAX_CONCURRENT_RUNS = var.runs
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
        cpu    = 256 * var.runs
        memory = 1024 * var.runs
      }
    }
  }
}
