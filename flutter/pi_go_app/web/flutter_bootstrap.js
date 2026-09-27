{{flutter_js}}
{{flutter_build_config}}

// Version the application entry point independently of the HTML/service worker.
// An older controlling worker may otherwise return its cached main.dart.js even
// when Chrome DevTools has "Disable cache" enabled (that toggle does not bypass
// service-worker CacheStorage).
for (const build of _flutter.buildConfig.builds) {
  if (build.mainJsPath) build.mainJsPath += "?v=17";
  if (build.mainWasmPath) build.mainWasmPath += "?v=17";
  if (build.jsSupportRuntimePath) build.jsSupportRuntimePath += "?v=17";
}

_flutter.loader.load({
  config: {
    // Keep the renderer on this origin so the installed PWA works offline.
    canvasKitBaseUrl: "canvaskit/"
  }
});
