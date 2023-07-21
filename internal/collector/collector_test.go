package collector

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	. "github.com/openshift/cluster-logging-operator/test/matchers"
	"path"

	"fmt"
	logging "github.com/openshift/cluster-logging-operator/apis/logging/v1"
	"github.com/openshift/cluster-logging-operator/internal/collector/fluentd"
	vector "github.com/openshift/cluster-logging-operator/internal/collector/vector"
	"github.com/openshift/cluster-logging-operator/internal/constants"
	"github.com/openshift/cluster-logging-operator/internal/utils"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"os"
)

var _ = Describe("Factory#NewPodSpec", func() {
	var (
		podSpec   v1.PodSpec
		collector v1.Container

		factory *Factory
	)
	BeforeEach(func() {
		factory = &Factory{
			CollectorType: logging.LogCollectionTypeFluentd,
			ImageName:     constants.FluentdName,
			Visit:         fluentd.CollectorVisitor,
		}
		podSpec = *factory.NewPodSpec(nil, logging.ClusterLogForwarderSpec{}, "1234", "")
		collector = podSpec.Containers[0]
	})
	Describe("when creating of the collector container", func() {

		It("should provide the pod IP as an environment var", func() {
			Expect(collector.Env).To(IncludeEnvVar(v1.EnvVar{Name: "POD_IP",
				ValueFrom: &v1.EnvVarSource{
					FieldRef: &v1.ObjectFieldSelector{
						APIVersion: "v1", FieldPath: "status.podIP"}}}))
		})
		It("should set a security context", func() {
			Expect(collector.SecurityContext).To(Equal(&v1.SecurityContext{
				Capabilities: &v1.Capabilities{
					Drop: RequiredDropCapabilities,
				},
				SELinuxOptions: &v1.SELinuxOptions{
					Type: "spc_t",
				},
				ReadOnlyRootFilesystem:   utils.GetBool(true),
				AllowPrivilegeEscalation: utils.GetBool(false),
				SeccompProfile: &v1.SeccompProfile{
					Type: v1.SeccompProfileTypeRuntimeDefault,
				},
			}))
		})
	})

	Describe("when creating the podSpec", func() {

		Context("and evaluating tolerations", func() {
			It("should add only defaults when none are defined", func() {
				Expect(podSpec.Tolerations).To(Equal(defaultTolerations))
			})

			It("should add the default and additional ones that are defined", func() {
				providedToleration := v1.Toleration{
					Key:      "test",
					Operator: v1.TolerationOpExists,
					Effect:   v1.TaintEffectNoSchedule,
				}
				factory.CollectorSpec = logging.CollectionSpec{
					Type: "fluentd",
					CollectorSpec: logging.CollectorSpec{
						Tolerations: []v1.Toleration{
							providedToleration,
						},
					},
				}
				podSpec = *factory.NewPodSpec(nil, logging.ClusterLogForwarderSpec{}, "1234", "")
				expTolerations := append(defaultTolerations, providedToleration)
				Expect(podSpec.Tolerations).To(Equal(expTolerations))
			})

		})

		Context("and evaluating the node selector", func() {
			It("should add only defaults when none are defined", func() {
				Expect(podSpec.NodeSelector).To(Equal(utils.DefaultNodeSelector))
			})
			It("should add the selector when defined", func() {
				expSelector := map[string]string{
					"foo":             "bar",
					utils.OsNodeLabel: utils.LinuxValue,
				}
				factory.CollectorSpec = logging.CollectionSpec{
					Type: "fluentd",
					CollectorSpec: logging.CollectorSpec{
						NodeSelector: map[string]string{
							"foo": "bar",
						},
					},
				}
				podSpec = *factory.NewPodSpec(nil, logging.ClusterLogForwarderSpec{}, "1234", "")
				Expect(podSpec.NodeSelector).To(Equal(expSelector))
			})

		})

		Context("and the proxy config exists", func() {

			var verifyEnvVar = func(container v1.Container, name, value string) {
				for _, elem := range container.Env {
					if elem.Name == name {
						Expect(elem.Value).To(Equal(value), "Exp. collector to have env var %s: %s:", name, value)
						return
					}
				}
				Fail(fmt.Sprintf("Exp. collector to include env var: %s", name))
			}

			var verifyProxyVolumesAndVolumeMounts = func(container v1.Container, podSpec v1.PodSpec, trustedca string) {
				found := false
				for _, elem := range container.VolumeMounts {
					if elem.Name == trustedca {
						found = true
						Expect(elem.MountPath).To(Equal(constants.TrustedCABundleMountDir), "VolumeMounts %s: expected %s, actual %s", trustedca, constants.TrustedCABundleMountDir, elem.MountPath)
						break
					}
				}
				if !found {
					Fail(fmt.Sprintf("Trusted ca-bundle VolumeMount %s not found for collector", trustedca))
				}

				for _, elem := range podSpec.Volumes {
					if elem.Name == trustedca {
						Expect(elem.VolumeSource.ConfigMap).To(Not(BeNil()), "Exp. the podSpec to have a mounted configmap for the trusted ca-bundle")
						Expect(elem.VolumeSource.ConfigMap.LocalObjectReference.Name).To(Equal(trustedca), "Volume %s: ConfigMap.LocalObjectReference.Name expected %s, actual %s", trustedca, elem.VolumeSource.ConfigMap.LocalObjectReference.Name, trustedca)
						return
					}
				}
				Fail(fmt.Sprintf("Volume %s not found for collector", trustedca))
			}

			It("should add the proxy variables to the collector", func() {
				_httpProxy := os.Getenv("HTTP_PROXY")
				_httpsProxy := os.Getenv("HTTPS_PROXY")
				_noProxy := os.Getenv("NO_PROXY")
				cleanup := func() {
					_ = os.Setenv("HTTP_PROXY", _httpProxy)
					_ = os.Setenv("HTTPS_PROXY", _httpsProxy)
					_ = os.Setenv("NO_PROXY", _noProxy)
				}
				defer cleanup()

				httpproxy := "http://proxy-user@test.example.com/3128/"
				noproxy := ".cluster.local,localhost"
				_ = os.Setenv("HTTP_PROXY", httpproxy)
				_ = os.Setenv("HTTPS_PROXY", httpproxy)
				_ = os.Setenv("NO_PROXY", noproxy)
				caBundle := "-----BEGIN CERTIFICATE-----\n<PEM_ENCODED_CERT>\n-----END CERTIFICATE-----\n"
				podSpec = *factory.NewPodSpec(&v1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "openshift-logging",
						Name:      constants.CollectorTrustedCAName,
					},
					Data: map[string]string{
						constants.TrustedCABundleKey: caBundle,
					},
				}, logging.ClusterLogForwarderSpec{}, "1234", "")
				collector = podSpec.Containers[0]

				verifyEnvVar(collector, "HTTP_PROXY", httpproxy)
				verifyEnvVar(collector, "HTTPS_PROXY", httpproxy)
				verifyEnvVar(collector, "NO_PROXY", "elasticsearch,"+noproxy)
				verifyProxyVolumesAndVolumeMounts(collector, podSpec, constants.CollectorTrustedCAName)
			})
		})
	})

})

var _ = Describe("Factory#CollectorResourceRequirements", func() {
	var (
		factory        *Factory
		collectionSpec = logging.CollectionSpec{
			CollectorSpec: logging.CollectorSpec{
				Resources: &v1.ResourceRequirements{
					Limits: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("120Gi"),
					},
					Requests: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("100Gi"),
						v1.ResourceCPU:    resource.MustParse("500m"),
					},
				},
			},
		}
		expResources = v1.ResourceRequirements{
			Limits: v1.ResourceList{
				v1.ResourceMemory: resource.MustParse("120Gi"),
			},
			Requests: v1.ResourceList{
				v1.ResourceMemory: resource.MustParse("100Gi"),
				v1.ResourceCPU:    resource.MustParse("500m"),
			},
		}
	)

	Context("when collectorType is vector", func() {
		BeforeEach(func() {
			factory = &Factory{
				CollectorType: logging.LogCollectionTypeVector,
				ImageName:     constants.VectorName,
				Visit:         vector.CollectorVisitor,
			}
		})
		It("should not define any resources when none are specified", func() {
			Expect(factory.CollectorResourceRequirements()).To(Equal(v1.ResourceRequirements{}))
		})

		It("should apply the spec'd resources when defined", func() {
			factory.CollectorSpec = collectionSpec
			Expect(factory.CollectorResourceRequirements()).To(Equal(expResources))
		})

	})
	Context("when collectorType is fluentd", func() {
		BeforeEach(func() {
			factory = &Factory{
				CollectorType: logging.LogCollectionTypeFluentd,
				ImageName:     constants.FluentdName,
				Visit:         fluentd.CollectorVisitor,
			}
		})
		It("should apply the default resources when none are defined", func() {
			Expect(factory.CollectorResourceRequirements()).To(Equal(v1.ResourceRequirements{
				Limits: v1.ResourceList{v1.ResourceMemory: fluentd.DefaultMemory},
				Requests: v1.ResourceList{
					v1.ResourceMemory: fluentd.DefaultMemory,
					v1.ResourceCPU:    fluentd.DefaultCpuRequest,
				},
			}))
		})
		It("should apply the spec'd resources when defined", func() {
			factory.CollectorSpec = collectionSpec
			Expect(factory.CollectorResourceRequirements()).To(Equal(expResources))
		})

	})
})

var _ = Describe("Factory#NewPodSpec Add Cloudwatch STS Resources", func() {
	var (
		factory   *Factory
		pipelines = []logging.PipelineSpec{
			{
				Name:       "cw-forward",
				InputRefs:  []string{logging.InputNameInfrastructure},
				OutputRefs: []string{"cw"},
			},
		}
		outputs = []logging.OutputSpec{
			{
				Type: logging.OutputTypeCloudwatch,
				Name: "cw",
				OutputTypeSpec: logging.OutputTypeSpec{
					Cloudwatch: &logging.Cloudwatch{
						Region:  "us-east-77",
						GroupBy: logging.LogGroupByNamespaceName,
					},
				},
				Secret: &logging.OutputSecretSpec{
					Name: "my-secret",
				},
			},
		}
		roleArn = "arn:aws:iam::123456789012:role/my-role-to-assume"
		secrets = map[string]*v1.Secret{
			// output secrets are keyed by output name
			outputs[0].Name: {
				Data: map[string][]byte{
					"credentials": []byte(roleArn),
				},
			},
		}
	)
	Context("when collectorType is fluentd", func() {
		BeforeEach(func() {
			factory = &Factory{
				CollectorType: logging.LogCollectionTypeFluentd,
				ImageName:     constants.FluentdName,
				Visit:         fluentd.CollectorVisitor,
				Secrets:       secrets,
			}
		})
		Context("when collector has a secret containing a credentials key", func() {

			It("should NO LONGER be setting AWS ENV vars in the container", func() {
				podSpec := *factory.NewPodSpec(nil, logging.ClusterLogForwarderSpec{
					Outputs:   outputs,
					Pipelines: pipelines,
				}, "1234", "")
				collector := podSpec.Containers[0]

				// LOG-4084 fluentd no longer setting env vars
				Expect(collector.Env).To(Not(IncludeEnvVar(v1.EnvVar{
					Name: constants.AWSRegionEnvVarKey,
				})))
				Expect(collector.Env).To(Not(IncludeEnvVar(v1.EnvVar{
					Name: constants.AWSRoleArnEnvVarKey,
				})))
				Expect(collector.Env).To(Not(IncludeEnvVar(v1.EnvVar{
					Name: constants.AWSRoleSessionEnvVarKey,
				})))
				Expect(collector.Env).To(Not(IncludeEnvVar(v1.EnvVar{
					Name: constants.AWSWebIdentityTokenEnvVarKey,
				})))
			})
		})
		Context("when collector has a secret containing a role_arn key", func() {
			BeforeEach(func() {
				factory.Secrets = map[string]*v1.Secret{
					outputs[0].Name: {
						Data: map[string][]byte{
							"role_arn": []byte(roleArn),
						},
					},
				}
			})
			It("should NO LONGER be setting AWS ENV vars in the container", func() {
				podSpec := *factory.NewPodSpec(nil, logging.ClusterLogForwarderSpec{
					Outputs:   outputs,
					Pipelines: pipelines,
				}, "1234", "")
				collector := podSpec.Containers[0]

				// LOG-4084 fluentd no longer setting env vars
				Expect(collector.Env).To(Not(IncludeEnvVar(v1.EnvVar{
					Name: constants.AWSRegionEnvVarKey,
				})))
				Expect(collector.Env).To(Not(IncludeEnvVar(v1.EnvVar{
					Name: constants.AWSRoleArnEnvVarKey,
				})))
				Expect(collector.Env).To(Not(IncludeEnvVar(v1.EnvVar{
					Name: constants.AWSRoleSessionEnvVarKey,
				})))
				Expect(collector.Env).To(Not(IncludeEnvVar(v1.EnvVar{
					Name: constants.AWSWebIdentityTokenEnvVarKey,
				})))
			})
		})
	})
	Context("when collectorType is vector", func() {
		BeforeEach(func() {
			factory = &Factory{
				CollectorType: logging.LogCollectionTypeVector,
				ImageName:     constants.VectorName,
				Visit:         vector.CollectorVisitor,
				Secrets:       secrets,
			}
		})
		Context("when collector has a secret containing a credentials key", func() {

			It("should find the AWS web identity env vars in the container", func() {
				podSpec := *factory.NewPodSpec(nil, logging.ClusterLogForwarderSpec{
					Outputs:   outputs,
					Pipelines: pipelines,
				}, "1234", "")
				collector := podSpec.Containers[0]

				Expect(collector.Env).To(IncludeEnvVar(v1.EnvVar{
					Name:  constants.AWSRegionEnvVarKey,
					Value: outputs[0].OutputTypeSpec.Cloudwatch.Region,
				}))
				Expect(collector.Env).To(IncludeEnvVar(v1.EnvVar{
					Name:  constants.AWSRoleArnEnvVarKey,
					Value: roleArn,
				}))
				Expect(collector.Env).To(IncludeEnvVar(v1.EnvVar{
					Name:  constants.AWSRoleSessionEnvVarKey,
					Value: constants.AWSRoleSessionName,
				}))
				Expect(collector.Env).To(IncludeEnvVar(v1.EnvVar{
					Name:  constants.AWSWebIdentityTokenEnvVarKey,
					Value: path.Join(constants.AWSWebIdentityTokenMount, constants.AWSWebIdentityTokenFilePath),
				}))
			})
		})
		Context("when collector has a secret containing a role_arn key", func() {
			BeforeEach(func() {
				factory.Secrets = map[string]*v1.Secret{
					outputs[0].Name: {
						Data: map[string][]byte{
							"role_arn": []byte(roleArn),
						},
					},
				}
			})
			It("should find the AWS web identity env vars in the container", func() {
				podSpec := *factory.NewPodSpec(nil, logging.ClusterLogForwarderSpec{
					Outputs:   outputs,
					Pipelines: pipelines,
				}, "1234", "")
				collector := podSpec.Containers[0]

				Expect(collector.Env).To(IncludeEnvVar(v1.EnvVar{
					Name:  constants.AWSRegionEnvVarKey,
					Value: outputs[0].OutputTypeSpec.Cloudwatch.Region,
				}))
				Expect(collector.Env).To(IncludeEnvVar(v1.EnvVar{
					Name:  constants.AWSRoleArnEnvVarKey,
					Value: roleArn,
				}))
				Expect(collector.Env).To(IncludeEnvVar(v1.EnvVar{
					Name:  constants.AWSRoleSessionEnvVarKey,
					Value: constants.AWSRoleSessionName,
				}))
				Expect(collector.Env).To(IncludeEnvVar(v1.EnvVar{
					Name:  constants.AWSWebIdentityTokenEnvVarKey,
					Value: path.Join(constants.AWSWebIdentityTokenMount, constants.AWSWebIdentityTokenFilePath),
				}))
			})
		})
	})
})
