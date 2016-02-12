jobsworth
=========

`jobsworth` is a small utility for creating dynamic
[Buildkite](https://buildkite.com/) pipelines based on a (very slightly)
higher-level description of a build and deploy process.

Buildkite's pipeline model is a flat list of steps that can either run a
command, wait for previous commands to complete, or block until a user
approves further process.

`jobsworth` defines a higher-level pipeline model with several phases:

* Smoke Test
* Build
* Deploy (to potentially many environments)
* Validate deployment (on each environment that was deployed to)

A `jobsworth` pipeline description looks something like this, assuming your
pipeline steps are implemented via a `Makefile`:

```yaml
smoke_test:
  - command: make test

build:
  - command: make buildkite-artifacts

deploy:
  - command: make ENV=${environment} CAUTIOUS=${cautious} buildkite-deploy
    name: ${environment}

validation_test:
  - command: make ENV=${environment} validation-test
    name: ${environment}

trivial_deploy_environments:
  - QA

cautious_deploy_environments:
  - PROD
```

The `smoke_test`, `build`, `deploy` and `validation_test` attributes are lists
of Buildkite command steps that will be added to the pipeline after some
minor adjustments.

The `smoke_test` and `build` steps are added first, and included only once.

The `deploy` and `validation_test` steps are added once for each of the
environments listed under `trivial_deploy_environments` and
`cautious_deploy_environments`, with the `${environment}` interpolation
replaced by the environment name.

`trivial_deploy_environments` and `cautious_deploy_environments` differ only
in that the "trivial" environments have their deploy and validate steps run
in parallel, assuming that they will be completely unattended, while the
"cautious" environments will run sequentially. The deploy steps themselves
may include differences based on the `${cautious}` flag, which will be
`1` in the cautious case and `0` otherwise.

Currently the transform is pretty rigid and designed around the workflow and
preferences at Say Media. In future we may make more of this configurable, but
at present that is not a goal. Further constraints are described in the
following sections.

Buildkite Queues and Agent Metadata
-----------------------------------

`jobsworth` presumes a number of different queue names for running Buildkite
agents, using different queues for each phase:

* `plan_pipeline` for situations where it wants to re-run itself for some reason
* `smoke_test` for the smoke test steps
* `build` for the build steps
* `deploy` for the deploy steps
* `validation_test` for the validation test steps

We also assume that your agents have in their metadata a key `environment`
that separates the deployment agents into a separate set per environment.
There is also the concept of a "build environment" which is where the
build steps themselves will run; the name of this must be provided in
an environment variable called `JOBSWORTH_ENVIRONMENT`.

Using `jobsworth` in Buildkite
------------------------------

`jobsworth` is intended to be used with Buildkite's dynamic pipeline upload
functionality. A Buildkite pipeline that uses `jobsworth` will usually have
only a single command step, that runs `jobsworth`:

```
jobsworth jobsworth.yml
```

If successful, `jobsworth` will set some metadata on the build and then upload
the generated pipeline.

Generating Additional Deployment Steps
--------------------------------------

The initial `jobsworth` config file contains only command steps, but those
steps can themselves generate further pipeline steps by uploading them
with the `buildkite-agent pipeline upload` command.

One reason you might want to do this is to make your "cautious" deploys stop
and wait for user approval before continuing:

```
cat <<EOT | buildkite-agent pipeline upload
steps:
  - block:
      label: "Approve Deploy"
  - command: make ENV=$JOBSWORTH_ENVIRONMENT deploy-for-real-this-time
EOT
```

By generating the blocking step dynamically you can skip it in cases where
there's nothing new to deploy.

Interpolation Variables
-----------------------

Strings within the configured steps can have the following variables
interpolated:

* `${environment}`: the name of the environment where the step will run.
  This is primarily useful on deploy steps.
* `${branch}`: the name of the git branch that the build belongs to
* `${codebase}`: a short name extracted from the git repository URL to
  identify the codebase. For `git@github.com:example/foo.git` this would be
  "foo".
* `${code_version}`: a version identifier for the git commit being built,
  in the format `YYYY-MM-DD-HHMMSS-xxxxxxx` where `xxxxxxx` is an abbreviated
  git commit id and the time/date fields are from that commit's creation
  timestamp.
* `${source_git_commit}`: the full id of the git commit that was current
  when `jobsworth` ran.
* `${cautious}`: expands as `1` for "cautious" deploy steps, and `0` for
  all other steps.

Environment Variables for Steps
-------------------------------

As a convenience to remove the need for excessive amounts of interpolation,
some environment variables are also set when running commands:

* `JOBSWORTH_ENVIRONMENT` is equivalent to `${environment}`
* `JOBSWORTH_CODEBASE` is equivalent to `${codebase}`
* `JOBSWORTH_CODE_VERSION` is equivalent to `${code_version}`
* `JOBSWORTH_CAUTIOUS` is equivalent to `${cautious}`

Rolling Back a Deployment
-------------------------

If you need to roll back, you want to re-release an artifact that was built
earlier, rather than building a new one.

`jobsworth` has special support for this. If you create a build via the
Buildkite UI and set its message to "Roll back to #12" then the generated
pipeline will omit the defined `smoke_test` and `build` steps and instead
synthesize a step that runs the command `jobsworth-copy-artifact-meta 12`,
which is expected to retrieve the relevant metadata from build 12 and copy
it into the current build.

The deploy steps will then be generated as normal, allowing them to operate
on the "stolen" artifact metadata.

Using the message as the trigger means that it will be clear in the Buildkite
summary UI when a given job is a rollback rather than a regular deployment.
Other text may appear after the "magic text" so you can explain the reason
for the rollback if desired: "Roll back to #12 to fix spline reticulation".

Deploying to a Custom Environment
---------------------------------

Perhaps you have other environments that are not in the usual release path
but will sometimes be deployed to for development or unusual testing reasons.

If you create a build via the Buildkite UI and set its message to
"Deploy to FOO" then the generated pipeline will ignore the environments
specified in the configuration and instead generate cautious deploy and
validate steps for the environment FOO.

If the message is instead set to "Deploy #12 to FOO", this will combine the
custom environment behavior with the rollback behavior to allow the artifacts
from an earlier build to be deployed to the given named environment.
