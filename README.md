# Generic Service

The goal of `Generic Service` is to quickly setup pipelines through simple config stages. Each stage describe an action with parameters for execution. Every stage result is accessible through template tags with the stage name.

Defining an `api` action at the end creates a `http route` which can be accessed. The parameters passed in can also be accessible through the `.variables` variable.

```sh
{{.variables.query_term}}
```

# Setup Instructions

I'm using mounted volume so setting the correct folder permission is important to read/write files.

```sh
chown 1000:1000 -R <folder>
```
