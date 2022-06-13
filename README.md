## e2e test for powervs hypershift hostedcluster

### setup instructions:
* Clone this repo where go 1.18 is installed
* set IBM Cloud authentication with below env variable
`export IBMCLOUD_API_KEY=<api_key>`
* create `config.json` with below values
`
  {
  "sshKeyPath": "<ssh public key file>",
  "pullSecret": "<pull secret file>"
  }
`
* run `./run.sh`