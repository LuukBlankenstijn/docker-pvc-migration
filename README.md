## docker-pvc-migration

Tool to easily migrate docker volume to k8s pvc's. It designed to be used together with [Kompose](https://github.com/kubernetes/kompose).
Kompose creates the yaml files for the pvc's, this tool looks at it displays a list of possible matching docker volumes. The user chooses one, can configure the size of the pvc, and this tool will apply the yaml files and copy over the data.

> WARNING:
> This was heavily vibe-coded
