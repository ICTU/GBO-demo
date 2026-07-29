// Monaco's web workers for GraphiQL 5's editor.
//
// GraphiQL ships this wiring itself (`graphiql/setup-workers/vite`), but that
// module lives in node_modules and uses Vite's `?worker` imports, which the
// dependency-optimizer cannot pre-bundle: excluding it from optimizeDeps then
// leaves the dev-server unable to spawn the workers, and monaco silently falls
// back to running the language services on the main thread. The same three
// imports from our own source are handled by Vite's worker plugin in both dev
// and build, so the editor keeps its workers either way.
//
// The monaco-editor version must match the one @graphiql/react depends on,
// otherwise two copies end up in the bundle.
//
// In the built image the workers run as workers. On the dev-server they do
// not: the pre-bundled monaco the page loads and the source monaco the worker
// entry pulls in are different copies, the worker fails to start, and monaco
// falls back to the main thread with a warning. Everything still works, the
// editor just does its parsing inline. Every combination of optimizeDeps
// exclude/include that gets the workers to start breaks something else
// (a CJS dependency, or `graphql` types crossing the worker boundary as two
// realms), so the fallback is the better trade for a dev-server.
import type { Environment } from 'monaco-editor'
import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker.js?worker'
import JsonWorker from 'monaco-editor/esm/vs/language/json/json.worker.js?worker'
import GraphQLWorker from 'monaco-graphql/esm/graphql.worker.js?worker'

const environment: Environment = {
  getWorker(_workerId: string, label: string) {
    switch (label) {
      case 'json':
        return new JsonWorker()
      case 'graphql':
        return new GraphQLWorker()
      default:
        return new EditorWorker()
    }
  },
}

// monaco declares `MonacoEnvironment` as a global `let`, which TypeScript will
// not hand out through globalThis — hence the cast rather than a plain
// assignment (which, from a module, would throw at runtime).
;(globalThis as unknown as { MonacoEnvironment: Environment }).MonacoEnvironment = environment
