{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "apollo-controller.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "apollo-controller.fullname" -}}
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

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "apollo-controller.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create the name of the service account to use
*/}}
{{- define "apollo-controller.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
    {{ default (include "apollo-controller.fullname" .) .Values.serviceAccount.name }}
{{- else -}}
    {{ default "default" .Values.serviceAccount.name }}
{{- end -}}
{{- end -}}

{{/*
Determine secret name, can either be the self-created of an existing one
*/}}
{{- define "apollo-controller.secretName" -}}
{{- if .Values.existingSecret.name -}}
    {{- .Values.existingSecret.name -}}
{{- else -}}
    {{ include "apollo-controller.fullname" . }}
{{- end -}}
{{- end -}}

{{/*
Determine configmap name, can either be the self-created of an existing one
*/}}
{{- define "apollo-controller.configName" -}}
{{- if .Values.existingConfig.name -}}
    {{- .Values.existingConfig.name -}}
{{- else -}}
    {{ include "apollo-controller.fullname" . }}
{{- end -}}
{{- end -}}

