export function passkeysSupported() {
  return window.isSecureContext && !!window.PublicKeyCredential && !!navigator.credentials;
}

function decode(value) {
  const base64 = value.replace(/-/g, '+').replace(/_/g, '/');
  return Uint8Array.from(atob(base64.padEnd(Math.ceil(base64.length / 4) * 4, '=')), (c) =>
    c.charCodeAt(0),
  );
}

function encode(value) {
  return btoa(String.fromCharCode(...new Uint8Array(value)))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '');
}

function creationOptions(options) {
  if (PublicKeyCredential.parseCreationOptionsFromJSON)
    return PublicKeyCredential.parseCreationOptionsFromJSON(options);
  return {
    ...options,
    challenge: decode(options.challenge),
    user: { ...options.user, id: decode(options.user.id) },
    excludeCredentials: (options.excludeCredentials || []).map((c) => ({ ...c, id: decode(c.id) })),
  };
}

function requestOptions(options) {
  if (PublicKeyCredential.parseRequestOptionsFromJSON)
    return PublicKeyCredential.parseRequestOptionsFromJSON(options);
  return {
    ...options,
    challenge: decode(options.challenge),
    allowCredentials: (options.allowCredentials || []).map((c) => ({ ...c, id: decode(c.id) })),
  };
}

function serialize(credential) {
  if (credential.toJSON) return credential.toJSON();
  const response = { clientDataJSON: encode(credential.response.clientDataJSON) };
  for (const key of ['attestationObject', 'authenticatorData', 'signature', 'userHandle']) {
    if (credential.response[key]) response[key] = encode(credential.response[key]);
  }
  if (credential.response.getTransports) response.transports = credential.response.getTransports();
  return {
    id: credential.id,
    rawId: encode(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment,
    clientExtensionResults: credential.getClientExtensionResults(),
    response,
  };
}

function passkeyError(error) {
  if (error.name === 'NotAllowedError' || error.name === 'AbortError')
    return new Error(
      'No se completó la verificación. Puedes intentarlo otra vez o usar otro dispositivo.',
    );
  if (error.name === 'InvalidStateError')
    return new Error('Esta passkey ya está registrada. Elige otra llave o dispositivo.');
  if (error.name === 'SecurityError')
    return new Error('Abre Nodes en su dominio HTTPS o en localhost para usar tu passkey.');
  return error;
}

export async function usePasskey(api, { register = false, remember = true, name = '' } = {}) {
  if (!passkeysSupported())
    throw new Error(
      'Este navegador no permite passkeys aquí. Usa un navegador compatible en HTTPS o localhost.',
    );
  const path = `/auth/passkeys/${register ? 'register' : 'login'}`;
  try {
    const options = await api(`${path}/begin`, {
      method: 'POST',
      retainSessionOn401: true,
      body: JSON.stringify(register ? { remember, name } : { remember }),
    });
    const credential = register
      ? await navigator.credentials.create({ publicKey: creationOptions(options.publicKey) })
      : await navigator.credentials.get({ publicKey: requestOptions(options.publicKey) });
    if (!credential) throw new Error('No se seleccionó una passkey. Inténtalo otra vez.');
    return await api(`${path}/finish`, {
      method: 'POST',
      retainSessionOn401: true,
      body: JSON.stringify(serialize(credential)),
    });
  } catch (error) {
    throw passkeyError(error);
  }
}
