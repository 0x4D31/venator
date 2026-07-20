job "venator-example" {
  datacenters = ["dc1"]
  type        = "batch"

  periodic {
    crons            = ["0 5 * * * *"]
    prohibit_overlap = true
    time_zone         = "Etc/UTC"
  }

  group "detection" {
    count = 1

    volume "venator-config" {
      type      = "host"
      source    = "venator-config"
      read_only = true
    }

    restart {
      attempts = 2
      interval = "30m"
      delay    = "30s"
      mode     = "fail"
    }

    task "venator" {
      driver = "docker"

      config {
        image           = "ghcr.io/0x4d31/venator:v0.2.0"
        readonly_rootfs = true
        cap_drop        = ["ALL"]
        args = [
          "run",
          "--global-config", "/etc/venator/files/global_config.yaml",
          "--rule-config", "/etc/venator/rules/example.yaml",
        ]
      }

      volume_mount {
        volume      = "venator-config"
        destination = "/etc/venator"
        read_only   = true
      }

      resources {
        cpu    = 250
        memory = 256
      }

      kill_timeout = "30s"
    }
  }
}
