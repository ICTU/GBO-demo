{{/* Expand the chart name. */}}
{{- define "gbo-app.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Create a stable, release-scoped resource name. */}}
{{- define "gbo-app.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/* Chart label value. */}}
{{- define "gbo-app.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Labels shared by every resource. */}}
{{- define "gbo-app.labels" -}}
helm.sh/chart: {{ include "gbo-app.chart" . }}
{{ include "gbo-app.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/* Immutable labels used by selectors. */}}
{{- define "gbo-app.selectorLabels" -}}
app.kubernetes.io/name: {{ include "gbo-app.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Name of the one-shot Job. Release-scoped like every other resource, but
suffixed with the release revision: a Job spec is immutable, so a release that
reruns a task has to create a new object rather than patch the old one. The
suffix also means `kubectl get jobs` reads as a history of attempts.
*/}}
{{- define "gbo-app.jobName" -}}
{{- $suffix := printf "-%v" .Release.Revision }}
{{- $base := include "gbo-app.fullname" . | trunc (int (sub 63 (len $suffix))) | trimSuffix "-" }}
{{- printf "%s%s" $base $suffix }}
{{- end }}

{{/*
The pod spec shared by the Deployment and the Job. One definition, so the two
release shapes cannot drift in how they mount volumes, drop capabilities or
read their environment. The parts that only make sense for a workload that
serves traffic — the container port and the probes — are omitted for a Job,
and `restartPolicy` is only set for one.
*/}}
{{- define "gbo-app.podSpec" -}}
{{- with .Values.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.podSecurityContext }}
securityContext:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- if .Values.job.enabled }}
restartPolicy: {{ .Values.job.restartPolicy }}
{{- end }}
{{- with .Values.initContainers }}
initContainers:
  {{- toYaml . | nindent 2 }}
{{- end }}
containers:
  - name: {{ .Chart.Name }}
    {{- with .Values.securityContext }}
    securityContext:
      {{- toYaml . | nindent 6 }}
    {{- end }}
    image: "{{ required "image.repository is required" .Values.image.repository }}:{{ default .Chart.AppVersion .Values.image.tag }}"
    imagePullPolicy: {{ .Values.image.pullPolicy }}
    {{- with .Values.command }}
    command:
      {{- toYaml . | nindent 6 }}
    {{- end }}
    {{- with .Values.args }}
    args:
      {{- toYaml . | nindent 6 }}
    {{- end }}
    {{- if not .Values.job.enabled }}
    ports:
      - name: http
        containerPort: {{ .Values.containerPort }}
        protocol: TCP
    {{- end }}
    {{- with .Values.env }}
    env:
      {{- toYaml . | nindent 6 }}
    {{- end }}
    {{- with .Values.envFrom }}
    envFrom:
      {{- toYaml . | nindent 6 }}
    {{- end }}
    {{- with .Values.volumeMounts }}
    volumeMounts:
      {{- toYaml . | nindent 6 }}
    {{- end }}
    {{- if and .Values.health.enabled (not .Values.job.enabled) }}
    livenessProbe:
      httpGet:
        path: {{ .Values.healthPath | quote }}
        port: {{ .Values.health.port | default "http" }}
        scheme: {{ .Values.health.scheme | upper | quote }}
      initialDelaySeconds: {{ .Values.health.initialDelaySeconds }}
      periodSeconds: {{ .Values.health.periodSeconds }}
      timeoutSeconds: {{ .Values.health.timeoutSeconds }}
      failureThreshold: {{ .Values.health.failureThreshold }}
    readinessProbe:
      httpGet:
        path: {{ .Values.healthPath | quote }}
        port: {{ .Values.health.port | default "http" }}
        scheme: {{ .Values.health.scheme | upper | quote }}
      initialDelaySeconds: {{ .Values.health.initialDelaySeconds }}
      periodSeconds: {{ .Values.health.periodSeconds }}
      timeoutSeconds: {{ .Values.health.timeoutSeconds }}
      failureThreshold: {{ .Values.health.failureThreshold }}
    {{- end }}
    resources:
      {{- toYaml .Values.resources | nindent 6 }}
{{- with .Values.volumes }}
volumes:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.nodeSelector }}
nodeSelector:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.affinity }}
affinity:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.tolerations }}
tolerations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end }}
