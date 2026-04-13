// Self-host fork: the upstream kernel's subscription model is gone. Every feature that
// used to be gated behind a b3log Pro subscription (cloud sync with third-party
// providers, inbox, spaced-repetition cloud, etc.) is unconditionally available now.
// Keeping the function signatures lets the rest of the app keep calling them without
// needing a large-surface refactor.

export const needSubscribe = (_tip?: string) => {
    // No subscription is ever required.
    return false;
};

export const isPaidUser = () => {
    // Every user of a self-hosted deployment has access to every feature.
    return true;
};
