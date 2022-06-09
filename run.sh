#!/bin/sh

curl https://raw.githubusercontent.com/canha/golang-tools-install-script/master/goinstall.sh | bash -s -- --version 1.18

curl -fsSL https://clis.cloud.ibm.com/install/linux | sh

curl https://mirror.openshift.com/pub/openshift-v4/clients/oc/latest/linux/oc.tar.gz -o oc.tar.gz

tar xzf oc.tar.gz

mv oc /usr/local/bin/
mv kubectl /usr/local/bin/

curl https://codeload.github.com/openshift/hypershift/zip/refs/heads/main -o hypershift.zip

unzip hypershift.zip

cd hypershift-main

CGO_ENABLED=0 GO111MODULE=on GOFLAGS=-mod=vendor go build -gcflags=all='-N -l' -o ./bin/hypershift .

cd ..

rm -f hypershift.zip oc.tar.gz

go build -o bin/hypershift-powervs-e2e

./bin/hypershift-powervs-e2e config.json