// graphql-voyager ships types for its standalone build under `typings/`, but
// nothing maps them onto the dist file we import (the package has no
// `exports` map). Point one at the other rather than falling back to `any`.
declare module 'graphql-voyager/dist/voyager.standalone.js' {
  export * from 'graphql-voyager/typings/standalone'
}
