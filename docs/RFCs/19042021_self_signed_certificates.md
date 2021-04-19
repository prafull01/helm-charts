Feature Name: Add a method to generate certificates without using Kubernetes CA
 
Status: Draft
 
Start Date: 19-04-2021
 
Authors: @prafull01, @abhisek-dwivedi, @madhurnawandar
 
# Summary
 
This RFC proposes a method of deploying the CockroachDB Helm chart in a secure mode by generating certificates without using Kubernetes CA.  
The new method allows users to specify their own CA or use self-generated CA.  
It handles the creation of Node and Client certificates using the CA.  
It allows rotation of CA, node, and client certificates.  
It allows specifying a duration for the generated certificates
 
# Motivation
 
Currently, CockroachDB supports 3 ways of cert management
* Built-in CSR's in Kubernetes: Depends on the user to approve CSR for all the generated certificates manually. CSR is no longer support with Certificates.k8s.io/v1 API and will be deprecated in Kubernetes v1.22. Many Kubernetes distributions like VMware Tanzu, EKS, etc are not allowing Kubernetes CA to sign the CSR's using Kubernetes CA.
* Cert-manager: This is the most efficient way of managing certificates, but not everyone uses cert-manager to manage the certificates. Also using cert-manager may be overkill for dev/test environments
* Manual: The user has to generate the certificates and provide them in the form of secrets. This method puts the overhead of certificate management leading to multiple manual steps for the user
 
CockroachDB user needs a default mechanism of cert management which should work on all the k8s distributions without the need for manual intervention. While cert-manager fits into the requirement, it makes it mandatory for the user to use a cert-manager. This new method of cert-management satisfies the user requirement without the need for 3rd party software like cert-manager.
 
## Goals
 
* Helm install command should be self-sufficient to launch the CockroachDB cluster in a secure mode.
 
* Dependency on external cert-manager should not be mandatory for creating CockroachDB cluster in a secure mode.
 
* Manual steps should not be required for creating the CockroachDB cluster in a secure mode.
  
## Non-Goals
 
* This RFC does not intend to fix the issues in the current default method of using Kubernetes CA
 
## Helm Configuration
This section specifies the suggested changes around user input in the Helm chart
 
1. Add option specifying CockroachDB to manage the certificates, `tls.certs.generate.enabled` as true/false.  
  Enabling this option will result in CockroachDB creating node and client certificates using a CA(either generated or provided by the user).
 
2. Add option specifying CockroachDB to use user provided CA, `tls.certs.generate.caProvided` as true/false.  
  Enabling this option will result in the generation of node and client certificates using the CA provided by the user.
 
3. Add option specifying the secret name containing user provide CA, `tls.certs.generate.caSecret`.  
  The secret name specified in this option will be used as a source for user-provided CA.
  This option is mandatory if the `tls.certs.generate.caProvided` is true.
 
4. Add option specifying the CA certificate expiration duration, `tls.certs.generate.caCertDuration`.  
  This duration will only be used when we create our own CA. The duration value from this option will be used to set the expiry of the generated CA certificate.
  By default, the CA expiry would be set to 10 years.
 
5. Add option specifying the client certificate expiration duration, `tls.certs.generate.clientCertDuration`.  
  The duration value from this option will be used to set the expiry of the generated client certificates. By default, client certificate expiry would be set to 1 year.
 
6. Add option specifying the node certificate expiration duration, `tls.certs.generate.nodeCertDuration`.  
  The duration value from this option will be used to set the expiry of the generated node certificates. By default, node certificate expiry would be set to 1 year.
 
7. Add option specifying CockroachDB to manage rotation of the generated certificates, `tls.certs.generate.rotateCerts` as true/false.  
  Enabling this option will result in auto-rotation of the certificates, before the expiry.
 
## Helm Input Validation
 
1. If `tls.certs.generate.caProvided` is set to true, then value for `tls.certs.generate.caSecret` must be provided.
 
2. If value for `tls.certs.generate.caSecret` is provided, secret should exist in the CockroachDB install namespace.
 
3. Value for `tls.certs.generate.caCertDuration` should be at least two months greater than the value for `tls.certs.generate.clientCertDuration`
  and `tls.certs.generate.nodeCertDuration`.
 
 
## Implementation Details
 
### Helm Components:
 
When `tls.certs.generate.enabled` is set to `true`, the following components are created for certificate generation and rotation:
 1. Certificate Management Service as a `pre-install` job.
 2. ServiceAccount, for `pre-install` job. (deleted after pre-install hook succeeds)
 3. Role, for adding a role to perform an operation on the secret resource. (deleted after pre-install hook succeeds)
 4. RoleBinding, for assigning permission to ServiceAccount for secret-related operation. (deleted after pre-install hook succeeds)
 5. Cron-job, for certificate rotation.  
 
### Helm Flow:   
* A `pre-install` [chart hook](https://helm.sh/docs/topics/charts_hooks/) will be used to create a job for the Certificate Management Service, that runs before all the Helm chart resources are installed.
  * This job will only run when `tls.certs.generate.enabled` is set to `true`.
  * This job will take care of generating all the required certificates.
  * Along with the `pre-install` hook job, serviceAccount, role, and rolebinding will also be created as part of `pre-install` hooks with different `hook-weight` so that the `pre-install`
 job has sufficient permissions to perform certificate generation.
 
   | Resource          | Hook-weight   | Order of Installation     |
   |----------------   |-------------  |-----------------------    |
   | ServiceAccount    | 1             | 1st                       |
   | Role              | 2             | 2nd                       |
   | RoleBindings      | 3             | 3rd                       |
   | Job               | 4             | 4th                       |
 
 * After all the `pre-install` hooks completed successfully, they will be deleted by hook deletion-policy defined in
 annotations.
 
 * All the certificate generation-related info will be passed on to the `pre-install` job as env variables.
    ```yaml
    env:
       - name: CA_CERT_DURATION
         value: {{ default 3650 .Values.tls.certs.generate.caCertDuration}}
       - name: NODE_CERT_DURATION
         value: {{ default 365 .Values.tls.certs.generate.nodeCertDuration}}
       - name: Client_CERT_DURATION
         value: {{ default 365 .Values.tls.certs.generate.clientCertDuration}}
       {{- if and (tls.certs.generate.caProvided .Values.tls.certs.generate.caSecret) }}
           {{- if not (lookup "v1" "Secret" ".Release.Namespace" ".Values.tls.certs.generate.caSecret")}}
           {{ fail "CA secret doesn't exist in cluster"}}
           {{- end }}
       - name: CA_SECRET
         values:
       {{- end }}
    ```
 
* 3 empty secret will be created in Helm chart for `cockroachdb-ca`, `cockroachdb-node` and `cockroachdb-root` if `tls.certs.generate.enabled`
 is set.
  * Data to these secrets will be populated in the `pre-install` job.
  * In case CA is provided by the user, then `cockroachdb-ca` secret is skipped.
  * Annotation is set on all the secrets created by CockroachDB; eg: `managed-by: crdb`
 
* A cronjob will be created in Helm chart when `tls.certs.generate.rotateCerts` is set.
  * This cronjob will run periodically to rotate the certificates.
  * The schedule of the cronjob will be of two months.
  * On every scheduled run, it will check if there is any certificate that is going to expire before the next scheduled run,
   if yes then it will renew the certificates.
  
  * <b>The cronjob will use the same `pre-install` job image for certificate rotations. The `pre-install` job image binary will have an argument `--rotate` for handling certificate rotation.</b>
 
* The Stateful pod will be changed to only run `copy-certs` initContainer to copy the certificates from nodeSecret to emptyDir volume.
 The rest of the main DB container flow will remain the same.
 - Client certificate generation process will remain the same as the current implementation using the `post-install` job.
 
`TODO: Need to identify how to generate the SIGHUP signal in all the nodes for certificate renewal`
 
### Certificate Management Service Implementation
 
 
### Overall Flow
 
* Check if CA secret is empty
  * if yes, [Generate-CA](####Generate-CA), [Generate-Node-Cert](####Generate-Node-Cert) and [Generate-Client-Cert](####Generate-Client-Cert); return
  * if not, [Validate Secret Annotations for CA](####Validate-Secret-Annotations)
    * if valid, CA is intact
    * if not, [Annotate-CA](####Annotate-CA), [Generate-Node-Cert](####Generate-Node-Cert) and [Generate-Client-Cert](####Generate-Client-Cert); return
* Check if [CA requires rotation](####Check-cert-for-regeneration)
   * if yes, follow [Rotate-CA](####Rotate-CA); return
* Check if node or client certificates [needs to be regenerated](####Check-cert-for-regeneration):
   * if yes, follow [Generate-Node-Cert](####Generate-Node-Cert) and [Generate-Client-Cert](####Generate-Client-Cert)
 
 
 
#### Generate-CA
* A self-signed CA will be generated
* Expiry of the certificate will be driven by the Helm value for `tls.certs.generate.caCertDuration`, passed as env variable
* Contents of the CA certificate will be stored to the CA secret as per value from `tls.certs.generate.caSecret`, passed as env variable
* An annotation `managed-by: crdb` will be added on Secret
* Follow [CA Annotation Workflow](####Annotate-CA)
 
#### Generate-Node-Cert
* A Node cert will be generated by signing it with the generated CA
* Contents of the Node cert will be stored to the Node secret as per value from `tls.certs.generate.nodeSecret`, passed as env variable
* An annotation `managed-by: crdb` will be added on Secret
* Node secret will be patched with annotation `resourceVersion` with the value of its current resourceVersion
* Node secret will be patched with annotation `creationTime` and `duration` with current UTC time and value from `tls.certs.generate.nodeCertDuration`
 
#### Generate-Client-Cert
* A Client cert will be generated by signing it with the generated CA
* Contents of the Client cert will be stored to the Client secret as per value from `tls.certs.generate.clientSecret`, passed as env variable
* An annotation `managed-by: crdb` will be added on Secret
* Client secret will be patched with annotation `resourceVersion` with the value of its current resourceVersion
* Client secret will be patched with annotation `creationTime` and `duration` with current UTC time and value from `tls.certs.generate.clientCertDuration`
 
#### Annotate-CA
* CA secret will be patched with annotation `resourceVersion` with the value of its current resourceVersion
* CA secret will be patched with annotation `creationTime` and `duration` with current UTC time and value from `tls.certs.generate.caCertDuration`
* Now that CA is generated, it will be followed by [Node cert generation workflow](####Node-cert-generation=workflow)
 
#### Rotate-CA
* CA rotation will follow the same workflow as [CA Generation Workflow](####Generate-CA), along with few additional steps as listed below
* Before storing the CA certificate to the CA secret, the new certificate will be bundled with the old certificate and the bundle will be stored
* In addition client secret and node secret will be patched with annotation `needsRegeneration: True`, which specifies that they need to be regenerated in the next cronjob run. This is done in accordance with the suggestion in CockroachDB [doc](https://www.cockroachlabs.com/docs/v20.2/rotate-certificates.html#why-rotate-ca-certificates-in-advance)
 
#### Check-node-cert-for-regeneration
* Follow checks from [Check cert for regeneration](####Check-cert-for-regeneration)
* In addition check if annotation `needsRegeneration: True` exists, if yes return True (This will be case when CA cert has rotated, but client and node certs are still to be recreated)
 
#### Check-cert-for-regeneration
* Check if the cert secret has annotation `managed-by: crdb`, if not return False
* Check if the cert is expiring in the next 2 months using the values from annotations `creationTime` and `duration` on Secret, if expiring return True, else False
 
#### Validate-Secret-Annotations
* Check if annotation for `resourceVersion` and `creationTime` exists, if not return False
* Check if resourceVersion of the Secret matches with the value of `resourceVersion` annotation, if not means Secret is changed, return False
* Else, return True
 
### Certificate Generation cases during Helm upgrade:
 
In case of Helm upgrade:
 
* User has given CA and changes contents of the CA secret:
  * Check if the current value `resourceVersion` or hash matches with the annotation value. if annotation does not match, so this is a new CA scenario.
* User has given CA and changes the secret name:
  * Annotation will not be found, so this is a new CA scenario.
* User had not given CA previously, but now has given the CA:
  * Annotation will not be found, so this is a new CA scenario
* If the user changes the duration of CA:
  * Identify and compare using existing annotation on CA secret and current value, this will be a case for certificate rotation. This will only be the case when the CA is managed by CockroachDB.
* User changes the duration of all certificates:
  * Compare old and new CA duration from CA secret annotation values and current value. This will be a case for certificate rotation.
  * Rotate CA certificate and add an annotation on CA secret with the date of rotation.
  * Add annotations on node and client certs specifying the new expected duration and `to-be-rotated: true`.
  * These secret certificates will be renewed in the next cron cycle and `to-be-rotated:true` and expected duration annotations will be removed.
* User only changes the duration of either node or client certificate:
  * Identify and compare duration with existing annotation value and current value and renew node or client certificate.
* User certificate management method is changed from cert generation to `cert-manager` or `default manual k8s CSR approval`:
  * Do nothing as this `pre-install` job  won't be triggered.
 
### Periodic Rotation scenarios:
* CA cert is near expiry: This will be identified using the generation time put on the CA secret. This will lead to a cert rotation scenario.
* Node or client cert near expiry: This will be identified using the generation time put on the respective secret. This will lead to regeneration of node or client certificate
 
### Certificate Rotation scenario:
 
* Only renew CA cert by combining new CA along with the old CA.
* Add annotation on CA secret with the date of rotation.
* Add annotations on node and client certs `to-be-rotated: true`.
* Do not process node and client cert.
* On the next scheduled iteration, if `to-be-rotated: true` annotation found, then we renew of node or client certificate remove the annotation from.
* Remove `to-be-rotated: true` from nodeSecret and client Secret.
