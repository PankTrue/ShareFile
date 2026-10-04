import { useCallback, useEffect, useState } from 'react';
import { api, errorMessage } from './api';

export function useResource<T>(path: string | null) {
  const [value, setValue] = useState<T>();
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [revision, setRevision] = useState(0);
  const refresh = useCallback(() => setRevision((v) => v + 1), []);
  useEffect(() => {
    const controller = new AbortController();
    setValue(undefined);
    setError('');
    setLoading(Boolean(path));
    if (!path) return () => controller.abort();
    api<T>(path, 'GET', undefined, controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) setValue(value);
      })
      .catch((error) => {
        if (!controller.signal.aborted) setError(errorMessage(error));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [path, revision]);
  return { value, error, loading, refresh, mutate: setValue };
}

export function useTask() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  async function run(work: () => Promise<void>) {
    if (busy) return;
    setBusy(true);
    setError('');
    try {
      await work();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return { busy, error, run };
}
