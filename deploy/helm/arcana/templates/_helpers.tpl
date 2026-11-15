{{- define "arcana.fullname" -}}
{{- printf "%s" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "arcana.labels" -}}
app.kubernetes.io/name: arcana
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "arcana.selectorLabels" -}}
app.kubernetes.io/name: arcana
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "arcana.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}

{{/* The configuration file: the chart's fixed settings win over .Values.config. */}}
{{- define "arcana.config" -}}
{{- $fixed := dict
      "data_dir" .Values.dataDir
      "server" (dict "listen" "0.0.0.0:7070" "metrics_listen" "0.0.0.0:9090" "destroy_on_shutdown" false)
      "runtime" (dict "driver" "docker" "docker" (dict "host" (printf "unix://%s" .Values.docker.socketPath))) -}}
{{- $config := mustDeepCopy .Values.config -}}
{{- if and (eq (dig "runtime" "docker" "oci_runtime" "runsc" $config) "runc") (not (dig "runtime" "docker" "allow_insecure_runtime" false $config)) -}}
{{- fail "config.runtime.docker.oci_runtime is runc, which shares the node's kernel; set allow_insecure_runtime: true only if you accept that, or use runsc" -}}
{{- end -}}
{{- $proxy := dig "network" "proxy_image" "" $config -}}
{{- if not $proxy -}}
{{- $_ := set $config "network" (mustMergeOverwrite (default (dict) (get $config "network")) (dict "proxy_image" (include "arcana.image" .))) -}}
{{- end -}}
{{- toYaml (mustMergeOverwrite $config $fixed) -}}
{{- end -}}
