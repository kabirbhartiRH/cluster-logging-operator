package loki

import (
	"fmt"
	"github.com/openshift/cluster-logging-operator/internal/api/observability"
	"strings"

	"github.com/openshift/cluster-logging-operator/internal/generator/vector/output/common/tls"

	. "github.com/openshift/cluster-logging-operator/internal/generator/framework"
	"github.com/openshift/cluster-logging-operator/internal/generator/vector/output/common"
	auth "github.com/openshift/cluster-logging-operator/internal/generator/vector/output/common/auth"

	obs "github.com/openshift/cluster-logging-operator/api/observability/v1"
	genhelper "github.com/openshift/cluster-logging-operator/internal/generator/helpers"
	. "github.com/openshift/cluster-logging-operator/internal/generator/vector/elements"
	"github.com/openshift/cluster-logging-operator/internal/generator/vector/helpers"
	vectorhelpers "github.com/openshift/cluster-logging-operator/internal/generator/vector/helpers"
	commontemplate "github.com/openshift/cluster-logging-operator/internal/generator/vector/output/common/template"
	"github.com/openshift/cluster-logging-operator/internal/utils/sets"
)

const (
	logType                          = "log_type"
	lokiLabelKubernetesNamespaceName = "kubernetes.namespace_name"
	lokiLabelKubernetesPodName       = "kubernetes.pod_name"
	lokiLabelKubernetesHost          = "kubernetes.host"
	lokiLabelKubernetesContainerName = "kubernetes.container_name"
	podNamespace                     = "kubernetes.namespace_name"
)

var (
	// DefaultLabelKeys contains the log entry keys that are used as Loki stream labels by default.
	DefaultLabelKeys = []string{
		logType,

		//container labels
		lokiLabelKubernetesNamespaceName,
		lokiLabelKubernetesPodName,
		lokiLabelKubernetesContainerName,
	}

	containerLabels = []string{
		lokiLabelKubernetesNamespaceName,
		lokiLabelKubernetesPodName,
		lokiLabelKubernetesContainerName,
	}

	requiredLabelKeys = []string{
		lokiLabelKubernetesHost,
	}
)

type Loki struct {
	ComponentID string
	Inputs      string
	TenantID    Element
	Endpoint    string
	LokiLabel   []string
	common.RootMixin
}

func (l Loki) Name() string {
	return "lokiVectorTemplate"
}

func (l Loki) Template() string {
	return `{{define "` + l.Name() + `" -}}
[sinks.{{.ComponentID}}]
type = "loki"
inputs = {{.Inputs}}
endpoint = "{{.Endpoint}}"
out_of_order_action = "accept"
healthcheck.enabled = false
{{kv .TenantID -}}
{{.Compression}}
{{end}}`
}

type LokiEncoding struct {
	ComponentID string
	Codec       string
}

func (le LokiEncoding) Name() string {
	return "lokiEncoding"
}

func (le LokiEncoding) Template() string {
	return `{{define "` + le.Name() + `" -}}
[sinks.{{.ComponentID}}.encoding]
codec = {{.Codec}}
{{end}}`
}

type Label struct {
	Name  string
	Value string
}

type LokiLabels struct {
	ComponentID string
	Labels      []Label
}

func (l LokiLabels) Name() string {
	return "lokiLabels"
}

func (l LokiLabels) Template() string {
	return `{{define "` + l.Name() + `" -}}
[sinks.{{.ComponentID}}.labels]
{{range $i, $label := .Labels -}}
{{$label.Name}} = "{{$label.Value}}"
{{end -}}
{{end}}
`
}

func (e *Loki) SetCompression(algo string) {
	e.Compression.Value = algo
}

func New(id string, o obs.OutputSpec, inputs []string, secrets observability.Secrets, strategy common.ConfigStrategy, op Options) []Element {
	if genhelper.IsDebugOutput(op) {
		return []Element{
			Debug(id, vectorhelpers.MakeInputs(inputs...)),
		}
	}
	componentID := vectorhelpers.MakeID(id, "remap")
	remapLabelID := vectorhelpers.MakeID(id, "remap_label")

	var tenantTemplate Element
	sink := Output(id, o, []string{remapLabelID}, "")
	if hasTenantKey(o.Loki) {
		lokiTenantID := vectorhelpers.MakeID(id, "loki_tenant")
		tenantTemplate = commontemplate.TemplateRemap(lokiTenantID, []string{remapLabelID}, o.Loki.TenantKey, lokiTenantID, "Loki Tenant")
		sink = Output(id, o, []string{lokiTenantID}, lokiTenantID)
	}

	if strategy != nil {
		strategy.VisitSink(sink)
	}

	return []Element{
		CleanupFields(componentID, inputs),
		RemapLabels(remapLabelID, o, []string{componentID}),
		tenantTemplate,
		sink,
		common.NewEncoding(id, common.CodecJSON),
		common.NewAcknowledgments(id, strategy),
		common.NewBatch(id, strategy),
		common.NewBuffer(id, strategy),
		common.NewRequest(id, strategy),
		Labels(id, o),
		tls.New(id, o.TLS, secrets, op),
		auth.HTTPAuth(id, o.Loki.Authentication, secrets, op),
	}
}

func Output(id string, o obs.OutputSpec, inputs []string, tenant string) *Loki {

	return &Loki{
		ComponentID: id,
		Inputs:      vectorhelpers.MakeInputs(inputs...),
		Endpoint:    o.Loki.URLSpec.URL,
		TenantID:    Tenant(o.Loki, tenant),
		RootMixin:   common.NewRootMixin(nil),
	}
}

func lokiLabelKeys(l *obs.Loki) []string {
	var keys sets.String
	if l != nil && len(l.LabelKeys) != 0 {
		keys = *sets.NewString(l.LabelKeys...)
	} else {
		keys = *sets.NewString(DefaultLabelKeys...)
	}
	// Ensure required tags for serialization
	keys.Insert(requiredLabelKeys...)
	return keys.List()
}

func lokiLabels(lo *obs.Loki) []Label {
	ls := []Label{}
	for _, k := range lokiLabelKeys(lo) {
		r := strings.NewReplacer(".", "_", "/", "_", "\\", "_", "-", "_")
		name := r.Replace(k)
		l := Label{
			Name:  name,
			Value: formatLokiLabelValue(k),
		}
		if k == lokiLabelKubernetesHost {
			l.Value = "${VECTOR_SELF_NODE_NAME}"
		}
		if k == lokiLabelKubernetesNamespaceName {
			l.Value = fmt.Sprintf("{{%s}}", podNamespace)
		}
		ls = append(ls, l)
	}
	return ls
}

func remapLabelsVrl(labels []string) string {
	k8sEventLabel := `
if !exists(.%s) {
  .%s = ""
}`
	sb := strings.Builder{}
	for _, v := range labels {
		sb.WriteString(fmt.Sprintf(k8sEventLabel, v, v))
	}
	return sb.String()
}

func formatLokiLabelValue(value string) string {
	if strings.HasPrefix(value, "kubernetes.labels.") || strings.HasPrefix(value, "kubernetes.namespace_labels.") {
		parts := strings.SplitAfterN(value, "labels.", 2)
		r := strings.NewReplacer("/", "_", ".", "_")
		key := r.Replace(parts[1])
		key = fmt.Sprintf(`\"%s\"`, key)
		value = fmt.Sprintf("%s%s", parts[0], key)
	}
	return fmt.Sprintf("{{%s}}", value)
}

func RemapLabels(id string, o obs.OutputSpec, inputs []string) Element {
	return Remap{
		ComponentID: id,
		Inputs:      helpers.MakeInputs(inputs...),
		VRL:         remapLabelsVrl(containerLabels),
	}
}

func Labels(id string, o obs.OutputSpec) Element {
	return LokiLabels{
		ComponentID: id,
		Labels:      lokiLabels(o.Loki),
	}
}

func hasTenantKey(l *obs.Loki) bool {
	return l != nil && l.TenantKey != ""
}

func Tenant(l *obs.Loki, tenant string) Element {
	if !hasTenantKey(l) {
		return Nil
	}
	return KV("tenant_id", fmt.Sprintf(`"{{ _internal.%s }}"`, tenant))
}

func CleanupFields(id string, inputs []string) Element {
	return Remap{
		ComponentID: id,
		Inputs:      helpers.MakeInputs(inputs...),
		VRL:         "del(.tag)",
	}
}
