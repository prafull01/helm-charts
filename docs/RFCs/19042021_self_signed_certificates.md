Feature Name: Add a method to generate certificates without using kubernetes CA

Status: Draft

Start Date: 19-04-2021

Authors: @prafull01, @abhisek-dwivedi, @madhurnawandar

# Summary

This RFC proposes a method of deploying the cockroach db helm chart in a secure mode by generating certificates without using kubernetes CA.  
The new method will allow users to specify their own CA otherwise CA will be generated as part of this method. Using the CA, node and client certificates will be generated. Rotation of CA, node and clinet certificates is also considered.  
This manual steps for any user could be error prone and might discourage users to run cockroach db in secure mode for test environments.  
Also, this RFC eliminates the need of kubernetes CA to sign our certificates. We will sign our own certificates and manage those certificates.

# Motivation

Currently cocroachdb support 3 ways of cert management
* Built-in CSR's in kubernetes: Depends on user to approve CSR for all the generated certificates manually. CSR is no longer support with Certificates.k8s.io/v1 API and will be deprecated in kubernetes v1.22. Many kubernetes distribution like VMware Tanzu, EKS etc are not allowing kubernetes CA to sign the CSR's using kubernetes CA. 
* Cert-manager: This is the most efficient way of managing certificates, but not everyone uses cert-manager to manage the certificates. Also using cert-manager may be an overkill for dev/test environments
* Manual: User has to generate the certificates and provide it in the form of secrets. This method puts the overhead of certificate management leading to multiple manual steps for the user

Cocroachdb user needs a default mechanism of cert management which should work on all the k8s distributions without the need for manual intervention. While cert-manager fits into the requirement, it makes it mandatory for the user to use a cert-manager. This new method of cert-management satisfies the user requirement without the need for 3rd party software like cert-manager. 

## Goals

* Helm install command should be self-sufficient to launch the cockroach db cluster in secure mode.

* Dependency on external cert-manager should not be mandatory for creating cockroach db cluster in secure mode.

* Manual steps should not be required for creating cockroach db cluster in secure mode.
    
## Non-Goals

* This RFC does not intend to fix the issues in the current default method of using kubernetes CA

## Helm Configuration
This section specifies the suggested changes around user input in helm chart

1. Add option specifying cockroach db to manage the certificates, `tls.certs.generate.enabled` as true/false.  
   Enabling this option will result into cockroach db creating node and client certificates using a CA(either generated or provided by user). 

2. Add option specifying cockroach db to use user provided CA, `tls.certs.generate.caProvided` as true/false.  
   Enabling this option will result into generation of node and client certificates using the CA provided by the user.
   
3. Add option specifying the secret name containing user provide CA, `tls.certs.generate.caSecret`.  
   The secret name specified in this option will be used as a source for user provided CA.
   This option is mandatory if the `tls.certs.generate.caProvided` is true.
   
4. Add option specifying the CA certificate expiration duration, `tls.certs.generate.caCertDuration`.  
   This duration will only be used when we create our own CA. The duration value from this option will be used to set the expiry of the generated CA certificate.
   By default, the CA expiry would be set to 10 years.
   
5. Add option specifying the client certificate expiration duration, `tls.certs.generate.clientCertDuration`.  
   The duration value from this option will be used to set the expiry of the generated client certificates. By default, client certificate expiry would be set to 1 year.

6. Add option specifying the node certificate expiration duration, `tls.certs.generate.nodeCertDuration`.  
   The duration value from this option will be used to set the expiry of the generated node certificates. By default, node certificate expiry would be set to 1 year.
   
7. Add option specifying cocroach db to manage rotation of the generated certificates, `tls.certs.generate.rotateCerts` as true/false.  
   Enabling this option will result into auto-rotation of the certificates, before the expiry.

## Helm Input Validation

1. If `tls.certs.generate.caProvided` is set to true, then value for `tls.certs.generate.caSecret` must be provided.
   
2. If value for `tls.certs.generate.caSecret` is provided, secret should exist in the cockroach db install namespace.
   
3. Value for `tls.certs.generate.caCertDuration` should be at least two months greater than the value for `tls.certs.generate.clientCertDuration` 
   and `tls.certs.generate.nodeCertDuration`.


## Implementation Details 

### Helm Components:

When `tls.certs.generate.enabled` is set to `true`, following components are created for certificate generation and rotation:
  1. Certificate Management Service as a `pre-install` job.
  2. ServiceAccount, for `pre-install` job. (deleted after pre-install hook succeeds)
  3. Role, for adding role to perform operation on secret resource. (deleted after pre-install hook succeeds)
  4. RoleBinding, for assigning permission to ServiceAccount for secret related operation. (deleted after pre-install hook succeeds)
  5. Cron-job, for certificate rotation.   

### Helm Flow:    
* A `pre-install` [chart hook](https://helm.sh/docs/topics/charts_hooks/) will be used to create a job for the Certificate Management Service, that runs before all the helm chart resources are installed. 
  * This job will only run when `tls.certs.generate.enabled` is set to `true`.
  * This job will take care of generating all the required certificates.
  * Along with the `pre-install` hook job, serviceAccount, role and rolebinding will also be created as part of `pre-install` hooks with different `hook-weight` so that the `pre-install`
  job have sufficient permissions to perform certificate generation.

    | Resource       	| Hook-weight 	| Order of Installation 	|
    |----------------	|-------------	|-----------------------	|
    | ServiceAccount 	| 1           	| 1st                   	|
    | Role           	| 2           	| 2nd                   	|
    | RoleBindings   	| 3           	| 3rd                   	|
    | Job            	| 4           	| 4th                   	|

  * After all the `pre-install` hooks completed successfully, they will be deleted by hook deletion-policy defined in
  annotations.

  * All the certificate generation related info will be passed on to the `pre-install` job as env variables.
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

* 3 empty secret will be created in helm chart for `cockroachdb-ca`, `cockroachdb-node` and `cockroachdb-root` if `tls.certs.generate.enabled`
  is set.
  * Data to these secrets will be populated in the `pre-install` job.
  * In case CA is provided by the user, then `cockroachdb-ca` secret is skipped.
  * Annotation is set on all the secrets created by cockroach db; eg: `managed-by: crdb`

* A cron-job will be created in helm chart when `tls.certs.generate.rotateCerts` is set.
  * This cron-job will run periodically to rotate the certificates.
  * The schedule of the cron-job will be of two months.
  * On every schedule run, it will check if there is any certificate which is going to expire before the next scheduled run, 
    if yes then it will renew the certificates.
    
  * <b>The cron-job will use the same `pre-install` job image for certificate rotations. The `pre-install` job image binary will
  have an argument `--rotate` for handling certificate rotation.</b>

* The Stateful pod will be cnhanges to only run `copy-certs` initContainer to copy the certificates from nodeSecret to emptyDir volume.
  Rest of the main db container flow will remain the same.
  
- Client certificate generation process will remain same as current implementation using the `post-install` job.

`TODO:Need to identify how to generate the SIGHUP signal in all the nodes for certificate renewal`

### Certificate Management Service Implementation

### Overall Flow

* Check if CA secret is empty
  * if yes, follow [CA Generation Workflow](####CA-generation-workflow), leading to generation of node and client certificates; return
  * if not, [Validate CA Secret Annotations](####Validate-Secret-Annotations)
    * if valid, CA is intact
    * if not, follow [CA Annotation Workflow](####CA-annotation-workflow), leading to generation of node and client certificates; return
* Check if [CA requires rotation](####Check-cert-for-regeneration)
    * if yes, follow [CA Rotation Workflow](####CA-rotation-workflow); return
* Check if node or client certificates [needs to be regenerated](####heck-cert-for-regeneration):
    * if yes, follow [Node cert generation workflow](####Node-cert-generation=workflow)
  

### Helper Workflows
#### Check-node-cert-for-regeneration
* Follow checks from [Check cert for rotation](####Check-cert-for-regeneration)
* In addition check if annotation `needsRegeneration: True` exists, if yes return True (This will be case when CA cert has rotated, but client and node certs are still to be recreated)


#### Check-cert-for-regeneration
* Check if the cert secret has annotation `managed-by: crdb`, if not return False
* Check if the cert is expiring in next 2 months using the values from annotations `creationTime` and `duration` on Secret, if expiring return True, else False


#### Validate-Secret-Annotations
* Check if annotation for `resourceVersion` and `creationTime` exists, if not return False
* Check if resourceVersion of the Secret matches with the value of `resourceVersion` annotation, if not means Secret is changed, return False
* Else, return True

#### CA-generation-workflow
* A self signed CA will be generated
* Expiry of the certificate will be driven by the Helm value for `tls.certs.generate.caCertDuration`, passsed as env variable
* Contents of the CA certificate will be stored to the CA secret as per value from `tls.certs.generate.caSecret`, passed as env variable
* An annotation `managed-by: crdb` will be added on Secret
* Follow [CA Annotation Workflow](####CA-annotation-workflow)

#### CA-annotation-workflow
* CA secret will be patched with annotation `resourceVersion` with value of it's current resourceVersion
* CA secret will be patched with annotation `creationTime` and `duration` with current UTC time and value from `tls.certs.generate.caCertDuration`
* Now that CA is generated, it will be followed by [Node cert generation workflow](####Node-cert-generation=workflow)

#### Node-cert-generation-workflow
* A node cert will be generated by signing it with the generated CA
* Contents of the node certificate will be stored to the node secret as per value from `tls.certs.generate.nodeSecret`, passed as env variable
* An annotation `managed-by: crdb` will be added on Secret
* Node secret will be patched with annotation `resourceVersion` with value of it's current resourceVersion
* Node CA secret will be patched with annotation `creationTime` and `duration` with current UTC time and value from `tls.certs.generate.caCertDuration`

#### CA-rotation-workflow
* CA rotation will follow the same workflow as [CA Generation Workflow](####CA-generation-workflow), along with few additional steps as listed below
* Before storing the CA certificate to the CA secret, the new certificate will be bundled with the old certificate and the bundle will be stored
* In addition client secret and node secret will be patched with annotation `needsRegeneration: True`, which specifies that they need to be regenerated in the next cron-job run. This is done in accordance with the suggestion in cockroachdb [doc](https://www.cockroachlabs.com/docs/v20.2/rotate-certificates.html#why-rotate-ca-certificates-in-advance) 


### Certificate Generation cases during helm upgrade:

In case of helm upgrade:

* User has given CA and changes contents of the CA secret:
  * Check if the current value `resourceVersion` or hash matches with the annotation value. if annotation does not match, so this is a new CA scenario.
* User has given CA and changes the secret name: 
  * Annotation will not be found, so this is a new CA scenario.
* User had not given CA previously, but now has given the CA:
  * Annotation will not be found, so this is a new CA scenario
* If user changes duration of CA: 
  * Identify and compare using existing annotation on CA secret and current value, this will be a case for certificate rotation. This will only 
    be case when the CA is managed-by cockroach db.
* User changes duration of all certificates: 
  * Compare old and new CA duration from CA secret annotation values and current value. This will be a case for certificate rotation. 
  * Rotate CA certificate and add an annotation on CA secret with the date of rotation. 
  * Add annotations on node and client certs specifying the new expected duration and `to-be-rotated: true`. 
  * These secret certificates will be renewed in next cron cycle and `to-be-rotated:true` and expected duration annotations will be removed.
* User only changes the duration of either node or client certificate:
  * Identify and compare duration with existing annotation value and current value and renew node or client certificate.
* User certificate management method is changed from cert generation to `cert-manager` or `default manual k8s CSR approval`:
  * Do nothing as this `pre-install` job  won't be triggered.

## Periodic Rotation scenarios:
* CA cert is near expiry: This will be identified using the generation time put on the CA secret. This will lead to cert rotation scenario.
* Node or client cert near expiry: This will be identified using the generation time put on the respective secret. This will lead to regeneration of node or client certificate

### Certificate Rotation scenario:

* Only renew CA cert by combining new CA along with the old CA.
* Add annotation on CA secret with the date of rotation.
* Add annotations on node and client certs`to-be-rotated: true`.
* Do not process node and client cert.
* On next scheduled iteration, if `to-be-rotated: true` annotation found , then we renew of node or client certificate remove the annotation from. 
* Remove `to-be-rotated: true` from nodeSecret and client Secret.


        
  * For all the generated certificates, their duration will be driven by the duration value set in the values.yaml.
    - Generated CA certificate life duration, default 10 years: `tls.certs.generate.caCertDuration`
    - Generated Node certificate life duration, default 1 year: ``tls.certs.generate.nodeCertDuration``
    - Generate Client certificate life duration, default 1 year: `tls.certs.generate.clientCertDuration`



* Annotation is set with `resourceVersion` or hash calculated on the CA secret (both user given CA and self-singed)
  * Annotation is set on all the secrets created by cockroach db; eg: `managed-by: crdb`
  * Annotations are set on CA, nodeSecret and clientSecret with the certificate generation time and duration info.
  * Empty secrets are added to allow proper cleanup during helm uninstall.



* If the CA is created by cockroach db:
    * check the CA certificate expiry and if the expiry is less than the next cronjob schedule,then do the CA certificate rotation.
    * If CA is rotated, then the node certificates and client certificates are rotated in the next scheduled run.

  * If the CA is provided by user:
    * CA cert rotation is not considered at all.
    * Check the for expiry of node certificates and client certificates. If certificate expiry is less than the next scheduled run,
      then do cert rotation.
