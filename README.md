Go Local
========

Go Local is an application to demonstrate docker compose setup with infrastructure dependencies and a Makefile to make running the tasks convenient in the local environment with a minimum of host installation dependencies.

## Development

Developing the application assumes you have [Docker](https://www.docker.com/) with [docker compose](https://docs.docker.com/compose/). This can be more easily installed on the developer workstation by installing [Docker Desktop](https://www.docker.com/products/docker-desktop/).

Managing the environment is done using dotenv files. Since the containers are run through docker-compose, the `.env` file is automatically parsed and passed into the container. The following is the list of environment variables that are accepted by the application:

* `APP_PORT`: the port to expose the app on, defaults to `8080`
* `AUTORELOAD`: enables autoreload of the application when the executable changes, defaults to `true`
* `DB_URL`: the url for connecting to the database, defaults to the db created by docker
* `POSTGRES_DB`: the postgres database name, defaults to `example`
* `POSTGRES_PASSWORD`: the postgres database password, defaults to `example`
* `POSTGRES_USER`: the postgres database user, defaults to `example`
* `REDIS_URL`: the url for connecting to the redis instance, defaults to the redis instance created by docker

Below are the common commands needed for local development. For a complete list of all commands, run `make help`

### Running

To run in the background, execute

```
$ make start
````

If instead you prefer to run in the foregorund, run in interactive mode by executing

```
$ make run
```

Once the application is running, it will be available on the specified port, `8080` by default. You can verify that it's running by hitting the `ping` endpoint:

```
$ curl http://localhost:8080/ping
```

If running in the background, you can view the logs by

```
$ make logs
```

### Building

To compile changes made to the application, run

```
$ make build
```

This will recompile the executable with your changes and restart the application if `AUTORELOAD` is enabled.

### Dependencies

Dependency management is done via [modules](https://go.dev/blog/using-go-modules). To introduce a new dependency, import it in your code like a regular dependency. Then

```
$ make build-dependencies
```

This will download the dependency and modify the `go.mod` file to include it.


### Shell

To connect to the container directly, run

```
$ make shell
```

Or to connect to the db shell, run

```
$ make shell-db
```

### Testing and Linting

To test and lint the application, run

```
$ make test
$ make lint
```
