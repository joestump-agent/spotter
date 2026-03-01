import React from 'react';
import BrowserOnly from '@docusaurus/BrowserOnly';

function colorForMethod(method: string): string {
  switch (method.toLowerCase()) {
    case 'get': return 'primary';
    case 'post': return 'success';
    case 'delete': return 'danger';
    case 'put': return 'info';
    case 'patch': return 'warning';
    case 'head': return 'secondary';
    case 'event': return 'secondary';
    default: return 'secondary';
  }
}

// SSR-safe override: the original calls useSelector (react-redux) at the top
// level, which fails during SSR because no Redux Provider wraps API pages
// (the classic DocItem layout is used, not ApiItem). The server URL portion
// is BrowserOnly in the original too, so omitting it here is safe.
export default function MethodEndpoint({
  method,
  path,
  context,
}: {
  method: string;
  path: string;
  context?: string;
}) {
  return (
    <>
      <pre className="openapi__method-endpoint">
        <span className={`badge badge--${colorForMethod(method)}`}>
          {method === 'event' ? 'Webhook' : method.toUpperCase()}
        </span>
        {' '}
        {method !== 'event' && (
          <h2 className="openapi__method-endpoint-path">
            <BrowserOnly>
              {() => <>{path.replace(/\{([a-z0-9-_]+)\}/gi, ':$1')}</>}
            </BrowserOnly>
          </h2>
        )}
      </pre>
      <div className="openapi__divider" />
    </>
  );
}
