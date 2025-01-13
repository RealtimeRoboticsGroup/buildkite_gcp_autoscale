#!/bin/bash

set -ex

mkdir -p $1/demoCA/private/

openssl req -new -nodes -x509 -newkey rsa:2048 -subj "/C=US/ST=California/O=Realtime Robotics Group/OU=CI/CN=realtimeroboticsgroup.org" -keyout $1/demoCA/private/cakey.pem -out $1/demoCA/cacert.pem -days 3650
openssl rsa -in $1/demoCA/private/cakey.pem -noout -text

openssl genrsa -out $1/serverkey.pem 2048             

openssl req -new -subj "/C=US/ST=California/O=Realtime Robotics Group/OU=CI/CN=realtimeroboticsgroup.org" -key $1/serverkey.pem -out $1/req.pem -nodes

echo 01 > $1/demoCA/serial
touch $1/demoCA/index.txt
mkdir -p $1/demoCA/newcerts/
pushd $1/

openssl ca -batch -in $1/req.pem -keyfile $1/demoCA/private/cakey.pem -cert $1/demoCA/cacert.pem -days 3650 -notext -out $1/servercert.pem

cp $1/demoCA/cacert.pem /usr/local/share/ca-certificates/ca_selfsigned.crt
# And load it
sudo update-ca-certificates --fresh
