{{- define "wfnexus.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "wfnexus.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "wfnexus.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "wfnexus.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "wfnexus.serverSelector" -}}
app.kubernetes.io/name: {{ include "wfnexus.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: server
{{- end -}}

{{- define "wfnexus.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "wfnexus.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "wfnexus.pvcName" -}}
{{- default (printf "%s-data" (include "wfnexus.fullname" .)) .Values.persistence.existingClaim -}}
{{- end -}}

{{/* carry env shared by server and runners. Arg: dict "root" $ "server" bool */}}
{{- define "wfnexus.carryEnv" -}}
{{- $c := .root.Values.carry -}}
{{- if $c.enabled }}
- name: CARRY_REPO
  value: {{ required "carry.repo is required when carry.enabled" $c.repo | quote }}
{{- with $c.s3Endpoint }}
- name: CARRY_S3_ENDPOINT
  value: {{ . | quote }}
{{- end }}
{{- with $c.toolPubkey }}
- name: CARRY_TOOL_PUBKEY
  value: {{ . | quote }}
{{- end }}
{{- if and .server $c.state }}
- name: CARRY_STATE
  value: {{ $c.state | quote }}
{{- end }}
{{- if $c.keyFileSecret }}
- name: CARRY_KEY_FILE
  value: /etc/carry/{{ $c.keyFileKey }}
{{- end }}
{{- if $c.awsCredentialsSecret }}
- name: AWS_ACCESS_KEY_ID
  valueFrom: { secretKeyRef: { name: {{ $c.awsCredentialsSecret }}, key: AWS_ACCESS_KEY_ID } }
- name: AWS_SECRET_ACCESS_KEY
  valueFrom: { secretKeyRef: { name: {{ $c.awsCredentialsSecret }}, key: AWS_SECRET_ACCESS_KEY } }
{{- end }}
{{- end }}
{{- end -}}
