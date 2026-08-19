// Test stub for `virtual:pwa-register/react`.
//
// That module is generated at build time by vite-plugin-pwa, which is
// registered in vite.config.ts but NOT in vitest.config.ts — there is no
// service worker under test. Without a stand-in, the import is unresolvable
// under vitest, and because App.tsx transitively imports ReloadPrompt, that
// made App.tsx (the application's root component) impossible to import in a
// test at all. vitest.config.ts aliases the virtual module here.
//
// The shape mirrors what ReloadPrompt destructures: a `needRefresh` tuple and
// an `updateServiceWorker` callback. It reports "no update waiting", which is
// the correct state for a test environment with no service worker.

interface RegisterSWOptions {
    onRegisteredSW?: (swUrl: string, registration?: ServiceWorkerRegistration) => void;
    onRegisterError?: (error: unknown) => void;
    onNeedRefresh?: () => void;
    onOfflineReady?: () => void;
}

export function useRegisterSW(options?: RegisterSWOptions) {
    // Nothing is registered here, so none of these callbacks can ever fire.
    // Discarded explicitly rather than named with an underscore: this
    // project's eslint reports a lone unused argument regardless of prefix.
    void options;
    return {
        needRefresh: [false, () => {}] as [boolean, (value: boolean) => void],
        offlineReady: [false, () => {}] as [boolean, (value: boolean) => void],
        updateServiceWorker: async (reloadPage?: boolean) => { void reloadPage; },
    };
}
