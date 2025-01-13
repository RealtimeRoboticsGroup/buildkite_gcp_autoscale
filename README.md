# Overview
This golang process polls buildkite for outstanding jobs, and spins up GCP spot instances to service the load.

These spot instances shut themselves down after ~5 minutes of no work to save money
