Feature: Use Patches to dynamically set resource values

  As a user of Crossplane
  I want to use patches to dynamically set resource values
  so that I can parameterize my resources

  Background:
    Given Crossplane is running in cluster
    Given provider xpkg.upbound.io/upbound/provider-dummy:v0.3.0 is running in cluster
    And CompositeResourceDefinition is present
      """
        apiVersion: apiextensions.crossplane.io/v1
        kind: CompositeResourceDefinition
        metadata:
          name: xrobots.dummy.crossplane.io
          labels:
            provider: dummy-provider
        spec:
          defaultCompositionRef:
            name: robots-test
          group: dummy.crossplane.io
          names:
            kind: XRobot
            plural: xrobots
          claimNames:
            kind: Robot
            plural: robots
          versions:
            - name: v1alpha1
              served: true
              referenceable: true
              schema:
                openAPIV3Schema:
                  type: object
                  properties:
                    spec:
                      type: object
                      properties:
                        color:
                          type: string
                      required:
                        - color
                    status:
                      type: object
                      properties:
                        color:
                          type: string
      """

  @wip
  Scenario: Patch managed resources - ToCompositeFieldPath
    Given Composition is present
      """
        apiVersion: apiextensions.crossplane.io/v1
        kind: Composition
        metadata:
          name: robots-test
          labels:
            crossplane.io/xrd: xrobots.dummy.crossplane.io
            provider: dummy-provider
        spec:
          compositeTypeRef:
            apiVersion: dummy.crossplane.io/v1alpha1
            kind: XRobot
          writeConnectionSecretsToNamespace: default
          resources:
            - name: robot
              base:
                apiVersion: iam.dummy.upbound.io/v1alpha1
                kind: Robot
                spec:
                  forProvider:
                    color: red
              patches:
                - type: ToCompositeFieldPath
                  fromFieldPath: spec.forProvider.color
                  toFieldPath: status.color
      """
    When claim gets deployed
      """
        apiVersion: dummy.crossplane.io/v1alpha1
        kind: Robot
        metadata:
          name: test-robot
        spec:
          color: red
      """
    Then claim becomes synchronized and ready
    And claim composite resource becomes synchronized and ready
    And composed managed resources become ready and synchronized
    And claim composite has .status.color set to red

  @wip
  Scenario: Patch managed resources - CombineToComposite
    Given Composition is present
      """
        apiVersion: apiextensions.crossplane.io/v1
        kind: Composition
        metadata:
          name: robots-test
          labels:
            crossplane.io/xrd: xrobots.dummy.crossplane.io
            provider: dummy-provider
        spec:
          compositeTypeRef:
            apiVersion: dummy.crossplane.io/v1alpha1
            kind: XRobot
          writeConnectionSecretsToNamespace: default
          resources:
            - name: robot
              base:
                apiVersion: iam.dummy.upbound.io/v1alpha1
                kind: Robot
                spec:
                  forProvider:
                    color: red
              patches:
                - type: CombineToComposite
                  combine:
                    variables:
                      - fromFieldPath: spec.forProvider.color
                      - fromFieldPath: spec.forProvider.color
                    strategy: string
                    string:
                      fmt: "%s-%s"
                  toFieldPath: status.color
      """
    When claim gets deployed
      """
        apiVersion: dummy.crossplane.io/v1alpha1
        kind: Robot
        metadata:
          name: test-robot
        spec:
          color: red
      """
    Then claim becomes synchronized and ready
    And claim composite resource becomes synchronized and ready
    And composed managed resources become ready and synchronized
    And claim composite has .status.color set to red-red

  @wip
  Scenario: Patch managed resources - FromCompositeFieldPath
    Given Composition is present
      """
        apiVersion: apiextensions.crossplane.io/v1
        kind: Composition
        metadata:
          name: robots-test
          labels:
            crossplane.io/xrd: xrobots.dummy.crossplane.io
            provider: dummy-provider
        spec:
          compositeTypeRef:
            apiVersion: dummy.crossplane.io/v1alpha1
            kind: XRobot
          writeConnectionSecretsToNamespace: default
          resources:
            - name: robot
              base:
                apiVersion: iam.dummy.upbound.io/v1alpha1
                kind: Robot
                spec:
                  forProvider: {}
              patches:
                - type: FromCompositeFieldPath
                  fromFieldPath: spec.color
                  toFieldPath: spec.forProvider.color
      """
    When claim gets deployed
      """
        apiVersion: dummy.crossplane.io/v1alpha1
        kind: Robot
        metadata:
          name: test-robot
        spec:
          color: red
      """
    Then claim becomes synchronized and ready
    And claim composite resource becomes synchronized and ready
    And composed managed resources become ready and synchronized
    And composed managed resource has .spec.forProvider.color set to red

  @wip
  Scenario: Patch managed resources - CombineFromComposite
    Given Composition is present
      """
        apiVersion: apiextensions.crossplane.io/v1
        kind: Composition
        metadata:
          name: robots-test
          labels:
            crossplane.io/xrd: xrobots.dummy.crossplane.io
            provider: dummy-provider
        spec:
          compositeTypeRef:
            apiVersion: dummy.crossplane.io/v1alpha1
            kind: XRobot
          writeConnectionSecretsToNamespace: default
          resources:
            - name: robot
              base:
                apiVersion: iam.dummy.upbound.io/v1alpha1
                kind: Robot
                spec:
                  forProvider: {}
              patches:
                - type: CombineFromComposite
                  combine:
                    variables:
                      - fromFieldPath: spec.color
                      - fromFieldPath: spec.color
                    strategy: string
                    string:
                      fmt: "%s-%s"
                  toFieldPath: spec.forProvider.color
      """
    When claim gets deployed
      """
        apiVersion: dummy.crossplane.io/v1alpha1
        kind: Robot
        metadata:
          name: test-robot
        spec:
          color: red
      """
    Then claim becomes synchronized and ready
    And claim composite resource becomes synchronized and ready
    And composed managed resources become ready and synchronized
    And composed managed resource has .spec.forProvider.color set to red-red
