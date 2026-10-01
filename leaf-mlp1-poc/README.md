# v92 — "novos" = Last Updated do RetroAchievements

- Order > "Recently updated on RA": ordena pela data em que o conjunto de
  conquistas foi mexido pela última vez (a coluna "Last Updated" do hub),
  a mais recente em cima. Sem essa data, usa a data de criação da conquista
  mais nova; por último, a data de lançamento.
- Selo NEW e Show > "Updated in the last 60 days" seguem a mesma data.
- Página do jogo: linhas "Released", "Set added" e "Last updated (NEW)".
- Jogos lidos antes são conferidos de novo em segundo plano para pegar a
  data de atualização.

# v91 — "novos" pela data de lançamento do RA

- "Newest first" e o selo NEW passam a usar a "Release Date" do
  RetroAchievements (a mesma coluna da página do hub), com a data de
  publicação do conjunto de conquistas só como reserva.
- A data vem do hub (campo releasedAt) e, se faltar, do
  API_GetGameExtended lido em segundo plano; jogos já lidos antes são
  conferidos de novo para pegar a data.
- Página do jogo: linhas "Released 2026-08-21 (NEW)" e "Set added".
- Filtro: "Newest (release date)" em Order e "New (last 60 days)" em Show.

# v90 — jogos novos no RA e jogos masterizados

- Filtro > Order: "Newest on RA first" (conjunto de conquistas publicado mais
  recentemente primeiro). Jogos com conjunto de até 60 dias ganham "NEW" no
  selo da lista, e a página mostra "Set added  2026-08-30 (NEW)".
- Seu progresso no RetroAchievements (API_GetUserCompletionProgress, lido em
  segundo plano a cada 15 min): jogos masterizados/completados aparecem em
  dourado na lista, com marca dourada na linha e "MASTERED" no selo; a
  página mostra "You  6 / 6 unlocked (MASTERED)" e o cartão verde diz
  "MASTERED: you unlocked all 6 achievements".
- Filtro > Show: "New on RA (60 days)", "Mastered by me", "Not mastered",
  "Played by me".

# v89 — lista sem o desenvolvedor

- A segunda linha de cada jogo na lista mostra só console · gênero ·
  conquistas; o desenvolvedor saiu de lá (continua na página do jogo, na
  linha "Developer", e a busca do filtro ainda encontra por ele).

# v88 — gênero, filtro por gênero, página sem cortes

- Gênero do RetroAchievements (API_GetGameExtended, lido em segundo plano
  junto com jogadores/desbloqueios): aparece na linha do jogo na lista
  ("Game Boy · RPG · 6 achievements"), na prévia e na página do jogo
  ("Genre").
- Filtro > "Genre": todos os gêneros do hub com a quantidade de jogos.
  Jogos com mais de um gênero ("Action, Platformer") entram em cada um.
- Página do jogo: valores longos (jogadores, Page) cortados com "..." em
  vez de passar da borda; "Players 1.299 (7.704 unlocks)"; cabeçalho com o
  mesmo espaçamento ("installed and verified").

# v87 — ordenar por popularidade no RA e por preço

- Filtro > Order, novas opções:
  - "Most played (RA)": mais jogadores no RetroAchievements primeiro;
  - "Most unlocks (RA)": mais conquistas desbloqueadas pela comunidade;
  - "Cheapest paid first": jogos pagos do mais barato ao mais caro (grátis
    e sem preço depois).
- Os números vêm do RA (API_GetGameExtended), lidos em segundo plano (um
  jogo a cada 1,5 s, atualizados uma vez por semana) e aparecem na página
  do jogo: "Played by 1.234 players · 45.678 unlocks".
- Jogo instalado: "Installed and verified: achievements will work. Press A
  to check for an update."; cabeçalho sem "Installed" repetido.

# v86 — aviso de atualização, Reinstall de verdade, topo da lista

- Se o RA deixar de aceitar o arquivo que você tem instalado (passou a
  suportar outra versão), depois de "Refresh RetroAchievements hub" o jogo
  fica "update needed" na lista, a página mostra "UPDATE NEEDED ..." em
  âmbar e o botão vira [A] Update: baixa a versão atual, confere o hash e
  SUBSTITUI o arquivo antigo (mesmo lugar, mesmo nome). O resumo do refresh
  diz quantos jogos instalados precisam de atualização.
- [A] Reinstall num jogo em dia: confere e avisa "Your installed file is the
  current one - nothing to update" (antes só dizia "Already installed").
- Página do jogo: a frase de status diz claramente "Press A to download.
  The file is checked against RetroAchievements before it is installed.";
  jogo pago não comprado diz que não pode ser baixado e some o [A].
- Topo da lista: mensagens curtas ("hidden: ...", resumo do refresh) somem
  depois de alguns segundos e nunca passam por cima do nome da conta.

# v85 — página do jogo reorganizada; comparação de versões corrigida

- Sem QR code. No lugar, à direita:
  - cartão de status colorido (verde instalado e verificado, azul pronto,
    âmbar atenção — pago, sem match, não verificado —, vermelho
    indisponível) com uma frase só;
  - linhas de informação: Achievements, RA tags, Price, Version (RA vX ·
    itch.io vY), Developer, Page, File;
  - uma linha de tags do itch.io;
  - About: só a descrição do jogo.
- Cabeçalho: desenvolvedor · console · estado.
- Versões: comparadas também quando o arquivo já tinha sido baixado antes
  (usa o "Version x.y.z" que o itch.io mostra e as versões guardadas). Os
  nomes de arquivo do RA são lidos se ainda não estavam salvos. Se nada disser
  a versão do itch.io, o app olha o arquivo uma vez de novo.
- "Demo gone" só quando nenhum arquivo grátis existe (antes a página paga do
  jogo completo fazia a demo parecer indisponível).

# v84 — informações mais claras na lista e na página do jogo

- Página do jogo organizada em linhas: "STATUS: ..." (uma frase que diz o que
  dá para fazer agora), depois os fatos (RA #, console, conquistas, tags do
  RA, preço, página) e "ABOUT THE GAME" com a descrição. Sem frases
  contraditórias ("demo não existe" + "demo disponível").
- Cabeçalho da página e prévia da lista mostram o estado ("demo no longer
  available", "installed · verified", preço) em vez de só "Free", sem o
  console repetido.
- Selo da lista mais curto: "GBA DEMO gone", "R$ 15,41", "bought",
  "no match".
- Texto vermelho legível também na linha selecionada.
- A última verificação vence: se a demo foi encontrada, o jogo não fica
  marcado como indisponível por um resultado antigo.
- QR: "Scan: RetroAchievements page" (é o endereço que ele abre).

# v83 — RA adicionou hash novo: o app reabre o jogo

- Settings > "Refresh RetroAchievements hub" relê os hashes. Quando um jogo
  ganha hash novo (ex. o RA passou a aceitar a v1.0.7):
  - "v1.0.6 gone", "demo gone", "no hash match", "no RA hash" voltam a ficar
    disponíveis; os arquivos já tentados são tentados de novo, e os nomes dos
    arquivos do RA (com as versões) são lidos de novo;
  - um jogo instalado SEM verificação cujo arquivo agora bate o hash passa a
    "verified" sozinho.
- O topo da lista mostra o resumo: "hub refreshed: N new game(s), M with new
  RetroAchievements hashes".

# v82 — versão com conquistas não existe mais

- O app lê a versão nos nomes dos arquivos de hash do RA ("(v1.0.6)") e a
  versão do que o itch.io oferece (o "Version 1.0.7" de cada arquivo e os
  nomes dos arquivos baixados). Se nenhum arquivo bater o hash e a versão do
  RA não estiver entre as oferecidas, o jogo fica "v1.0.6 gone" em vermelho,
  sem [A] Download, e a página explica: "RetroAchievements supports v1.0.6,
  but itch.io now only offers v1.0.7". Se achar a versão antiga, fixe com
  --hub-set-url. Vale para demos e jogos completos.

# v81 — página da demo x página do jogo completo

- Quando o desenvolvedor tem as duas páginas (Goodboy Galaxy DEMO e Goodboy
  Galaxy (GBA)), a entrada Demo do RA fica com a página da demo e a entrada
  completa com a página do jogo completo. Antes a página completa (título
  exato, paga) ganhava, e a demo aparecia como "não existe mais".
- Arquivos de outras plataformas na mesma página não são baixados:
  "for PC (packed emulator)", "(cia)" do 3DS, Switch, Android.
- Jogos marcados "demo gone" são procurados de novo uma vez com a regra nova.

# v80 — demo que não existe mais em vermelho; preço lido da página

- Demo (RA) cujo arquivo grátis não existe mais: "demo gone" em vermelho na
  lista (com marca vermelha na linha), aviso na página do jogo, sem [A]
  Download. Não procura mais versão completa (o jogo completo, se tiver
  conquistas, vem como outra entrada do hub).
- Preço: quando o resultado da busca não mostra preço, o app lê a página do
  jogo. Jogo pago (ex. Good Boy Galaxy completo) não é mais tratado como
  grátis — antes os arquivos grátis da página (a demo) eram baixados para a
  versão completa.
- Página com demo e jogo completo: a entrada Demo do RA baixa primeiro o
  arquivo com "demo" no nome; a entrada completa deixa os de demo por último.

# v79 — demo grátis + jogo completo pago

- Jogos marcados como Demo no RA: a varredura verifica se a demo grátis
  ainda existe e se há uma versão completa paga (página e preço).
  - Lista: "GB DEMO free · full R$ 27,45", "demo gone · full R$ 27,45",
    "free · full owned".
  - Página do jogo: diz se a demo grátis existe (A baixa a demo, que é a que
    tem as conquistas) e o preço do jogo completo.
  - Se você COMPROU o jogo completo, aparece [X] Full game: instala o jogo
    completo ao lado da demo ("Título (Full game).ext"), sem conquistas (o RA
    não reconhece esse arquivo). Sem compra, essa opção não aparece.

# v78 — demo que não existe mais

- Jogo marcado como Demo no RA cuja página no itch.io agora vende o jogo
  completo: o app procura na página um arquivo de demo gratuito (o itch.io
  permite demo grátis em página paga). Se não houver, o jogo fica como
  "demo gone" na lista, com a explicação na página do jogo (o conjunto de
  conquistas é da demo; o jogo completo não é reconhecido pelo RA). Y
  esconde o jogo; --hub-set-url fixa um arquivo da demo achado em outro lugar.
- Extras que não são jogo não são mais baixados: diorama, papercraft,
  pôster, livro de colorir, PDF, press kit.

# v77 — cabeçalho, rolagem do Settings, descrição limpa

- "RA HUB" virou uma etiqueta e não cobre mais "All Systems" (era medido
  com a fonte pequena e desenhado com a grande).
- Settings / Hidden systems / Hidden games: a tela rola exatamente uma linha
  por toque quando a seleção chega embaixo (antes a conta ignorava a altura
  dos títulos de seção e era preciso apertar várias vezes).
- Descrição sem o texto padrão do itch.io ("100% of donations go to support
  the itch.io platform.", "()", aspas vazias).

# v76 — demos marcadas, Filtro e Downloads rápidos, visual

- Demos: o RetroAchievements marca no título (~Demo~). A lista mostra
  "DEMO" no selo, a prévia mostra as etiquetas do RA (Homebrew · Demo ·
  Hack...), a página do jogo avisa "This is a DEMO" e tem a etiqueta nos
  chips. Filtro > Show: "Full games (no demos)" e "Demos only".
- Filtro, Downloads e a tela de sair ficaram rápidos: a lista de fundo é
  desenhada uma vez numa textura e reaproveitada (o renderizador do
  aparelho é por software; antes a lista inteira era redesenhada a cada
  tecla). Busca de jogo por ID agora é instantânea.
- Visual: faixa de cabeçalho com "RA HUB" em destaque, linha de destaque
  sob o cabeçalho, textos de sair atualizados.

# v75 — esconder sistemas inteiros

- Settings > "Hidden systems": lista todos os consoles do hub com a
  quantidade de jogos; A alterna entre "shown" e "HIDDEN". Um sistema
  escondido some da lista, do filtro de sistemas, das contagens e da
  varredura do itch.io até ser mostrado de novo ali.
- Telas de Settings rolam para manter a linha selecionada visível.

# v74 — SNES na pasta certa, NES com conquistas, rodapé nítido

- SNES ia para /roms/sufami (Sufami Turbo), que só aceita .smc — por isso o
  .sfc virava .smc. Agora a pasta do sistema cujo NOME DE PASTA é o do
  console tem prioridade (/roms/snes, que aceita .sfc). Jogos já instalados
  no lugar errado são movidos na próxima abertura.
- NES: o Nestopia do dArkOS faz o RetroArch dizer "core not supported" para
  RetroAchievements. Sem escolha de core feita no EmulationStation, o app abre
  NES/Famicom/FDS com o FCEUmm (core de referência do RA) se estiver
  instalado.
- Rodapé: o texto não é mais reduzido (ficava borrado). Se não couber, o
  espaçamento diminui e, se preciso, os itens menos importantes saem,
  mantendo sempre o último (Back/Exit).

# v73 — rodapé igual em todas as telas

- Todas as telas (lista, filtro, teclado, downloads, settings, jogos
  escondidos, confirmação de apagar) usam o rodapé em botões da página do
  jogo: [A] Open  [SELECT] Filter  [Y] Clear filter ... Textos encurtados,
  navegação óbvia (direcional) fora da lista. Se não couber, reduz por igual.

# v72 — DS/SNES/NES baixam, PSP .cso verificado, MSX BIOS, rodapé novo

- Downloads de .nds, .nes, .sfc, .smc, .tar, .gz: o cliente herdado do Leaf
  (só Game Boy) descartava esses arquivos da lista da página — por isso
  "0 known ROM(s), 0 unknown-format file(s)" em jogos de DS. Arquivos com
  "emulator" no nome não são mais pulados ("Nintendo DS Emulator.zip" era a
  ROM).
- PSP em .cso: o app descomprime na hora para conferir o hash do RA e instala
  o .cso (o PPSSPP lê .cso direto).
- MSX: se o emulador fechar com erro e não houver BIOS em /roms/bios, a página
  do jogo explica o que falta (pastas 'Databases' e 'Machines' do
  blueMSXv282full.zip, ou os ROMs de BIOS do fMSX).
- Rodapé da página do jogo em "botões": [A] Download  [Y] Hide
  [SELECT] Search again  [X] Downloads  [B] Back. A navegação (direcional,
  L/R) não é mais listada. Se não couber, a linha inteira é reduzida por igual.

# v71 — rodapé cabe na tela, capas mais estáveis

- Rodapé da página do jogo: textos mais curtos ("A Download  Y Hide ...") e,
  se ainda não couber, o texto é comprimido para caber na largura da tela.
- Capas: erro de rede ("connection reset by peer") tenta de novo até 3
  vezes; se ainda falhar, tenta outra vez depois de 90 s, em vez de desistir
  até fechar o app.
- Conexões (capas e itch.io) usam IPv4 quando possível: os resets vinham
  todos da rota IPv6.

# v70 — esconder jogos

- Página de um jogo não instalado: Y pede confirmação, Y de novo esconde o
  jogo. Ele sai da lista, das contagens (hub, instalados, relatório) e da
  varredura do itch.io.
- Só volta por Settings > "Hidden games": A em um jogo mostra de novo;
  "Show all hidden games" traz todos.
- (Em jogo instalado o Y continua sendo PLAY.)

# v69 — capa e descrição de páginas fora do itch.io

- Página fixada com --hub-set-url (site do desenvolvedor): a imagem da
  página (og:image) vira a capa do jogo e os parágrafos da página (história,
  controles) viram a descrição. Já aparece ao fixar, sem esperar a varredura.
- Nas páginas do itch.io fixadas à mão, a capa também vem da página quando
  ainda não havia uma.

# v68 — página fora do itch.io

- --hub-set-url <ID> <URL> (o antigo --hub-set-itch continua funcionando)
  aceita qualquer página: itch.io, o site do desenvolvedor (ex.
  https://wls.hu/blinkysrevenge-gbc/) ou o link direto do arquivo. Em sites
  fora do itch, o app procura na página os links de download (.zip, .7z,
  extensões de ROM) e baixa direto. O hash do RA é conferido igual.

# v67 — downloads diretos, preço em reais, re-busca só do que falhou

- Páginas grátis com botão "Download" direto em cada arquivo (sem "Download
  Now"/nome-seu-preço), como a ROM do Slender, agora baixam. Antes o app
  dizia "no public download" e a mensagem falava em "pago".
- Preço em reais: convertido do preço do itch.io com a cotação do dia
  (open.er-api.com, salva em fx-rates.json). Na lista, na página e no filtro.
- Re-busca só dos jogos com problema:
  - na página do jogo: SELECT = procurar este jogo de novo agora
    (quando aparece o aviso de hash, SELECT continua sendo "instalar sem
    verificar");
  - Settings > "Re-scan games not found";
  - terminal: --hub-rescan [ID ...] (sem ID: todos os não encontrados).
  Mudanças na busca não refazem mais o hub inteiro, só os não encontrados.
- Vários desenvolvedores ("TLT; Tomahome"): busca "título dev1 dev2".
  Desenvolvedor creditado no texto da página ("The developers: TLT, ...")
  conta como o mesmo desenvolvedor (páginas de grupos como gcup.itch.io).
- Google consultado como navegador de texto (Lynx), que recebe HTML simples.
- Uploads ignorados: Linux 64-bit, x64/x86, source code/GitHub, emuladores,
  trilha sonora etc.
- Citação só no texto de outra página do mesmo autor vale menos
  ("veja também Dottie Flowers" em outro jogo).

# v66 — capas voltam

- Desfeita a troca da capa original pela miniatura 315x250: no itch.io o fim
  do endereço da imagem é uma assinatura daquele tamanho, então o endereço
  trocado dava 404. Agora usa a imagem original, e o leitor de imagem aceita
  figuras grandes (até 2600x2600) e reduz para a tela.
- Quando a página veio de uma grade (busca do itch, página do desenvolvedor),
  a miniatura da grade é usada.
- Capas salvas com o endereço errado são refeitas pela varredura.

# v65 — versão de navegador, página da ROM, capas grandes

- Página "(Browser)" / HTML5 / web version perde 50 pontos: não tem ROM.
- Links para outras páginas do mesmo autor dentro da página ("Want a copy?
  Download the ROM! https://jwgllc.itch.io/slender-the-8-gb-pages") são
  abertos e avaliados — é assim que a página certa do Slender é achada.
- Página sem preço e sem download público não é mais marcada como "paga"
  (costuma ser só versão de navegador).
- Capas enormes (1478x1476, recusadas pelo leitor de imagem) usam a miniatura
  315x250 do itch.io.
- Desenvolvedor com o nome em outra ordem ("Davy Willems" = "Willems Davy").
- Ordem dos buscadores: Bing, DuckDuckGo, Yahoo, Google, Startpage, Brave
  (Mojeek removido: sempre 403).
- Busca refeita automaticamente para os jogos não instalados.

# v64 — Silver Falls, Pokemon Mini, pastas do dArkOS

- Pokemon Mini: a pasta do dArkOS é /roms/pokemonmini (o app procurava
  "pokemini"). Tabela de pastas revisada com a lista do dArkOS
  (turbografx, turbografxcd, odyssey2, apple2...). Se o nome não bater, o app
  procura o sistema pelo nome completo no es_systems.cfg.
- Título com palavras a mais no meio conta ("Silver Falls Mini (Monsters In
  North Island)" = "Silver Falls: Monsters in North Island").
- Página que diz ser de outra plataforma no texto ("finally arrives on the
  PSVita") perde pontos.
- Acentos: "Pokémon Mini", "László" agora comparam certo.
- Nota de plataforma no título não atrapalha ("Double Symbol for Sega
  Genesis / Mega Drive / 32X" = "Double Symbol").
- Desenvolvedor "A | B", "A & B", "A / B" vira dois nomes.
- Busca refeita automaticamente para os jogos não instalados.

# v63 — só aceita página CONFIRMADA; desenvolvedor que mudou de nome

- Uma página só é aceita se for do mesmo desenvolvedor (nome do RA, nomes dos
  arquivos de hash, ou a conta para onde ele mudou) — ou, quando não dá para
  checar o desenvolvedor, título certo + console citado na página. Título
  parecido de outra pessoa nunca é baixado ("Casanova" de 1 GB para PC).
- Busca rápida: se nada confirmado, abre até 3 páginas promissoras e avalia
  de novo pelo texto completo.
- Desenvolvedor renomeado: página vazia que aponta para outra conta
  ("Health Potion Studios is now Distracted Coder") é seguida, e o nome novo
  passa a valer como o mesmo desenvolvedor.
- Download cancelado se o arquivo for grande demais para o console
  (96 MB para cartucho, 2 GB para disco).
- Instalar sem verificar agora é no SELECT (o A só faz a instalação
  verificada) — nada de baixar sem querer apertando A de novo.
- Corrigido: "Hong Kong 2099 for gameboy" perdia pontos ("número diferente",
  "outra plataforma"); GBC aceita páginas que dizem só "Game Boy".
- Resultados da busca antiga (menos rígida) são descartados automaticamente
  para jogos não instalados e buscados de novo.

# v62 — START fecha Settings; menos falsos positivos

- START dentro de Settings fecha a tela (além de B).
- Casos do log do aparelho:
  - "top web result" sozinho não basta: precisa do mesmo desenvolvedor ou do
    console E de uma palavra do título na página (não aceita mais "Health
    Potion Bottle" para "Go Catch 'Em", nem outro jogo do mesmo dev).
  - Página que cita o título COMPLETO vale mais que uma que cita só parte
    ("Silver Falls Mini (Monsters In North Island)" à frente das de 3DS).
  - Título que indica outra plataforma (3DS, PSVita, Android, PC...) perde
    pontos.
- Jogos já com página errada no cache: Settings > "Re-scan itch.io".

# v61 — página repetida: vale a MELHOR avaliação

- Bug: a mesma página chegava duas vezes (primeiro como item da lista do
  desenvolvedor, com nota 0; depois aberta e lida, com nota 115) e o
  ranking ficava com a PRIMEIRA. Agora fica com a melhor.
- Até 8 jogos da página do desenvolvedor são abertos (antes 4).

# v60 — correções vindas da página real do Saint Seiya (KOTZ)

- Página sem og:title (como a do Saint Seiya) era descartada como "não é
  página de jogo". Agora usa twitter:title / <title>.
- Texto da página inclui os nomes dos arquivos para download e toda a
  descrição (texto em inglês depois do espanhol); o subtítulo do RA sozinho
  também conta ("The Phoenix Returns").
- Página do desenvolvedor também como <nome>games / <nome>game / <nome>dev
  (zeichigames.itch.io); jogos desse desenvolvedor com outro nome para o
  mesmo console são abertos e avaliados pelo texto.
- Uploads com o mesmo nome dos arquivos de hash do RA, ou com o título do
  RA no nome, são baixados primeiro (a versão ENGLISH antes das outras
  línguas). Até 6 downloads por jogo.

# v59 — cada link da web é aberto e avaliado na hora

- Antes, um "match" pelo endereço fazia a busca parar no Bing, e a página
  aberta depois podia ser recusada — sem tentar o Google. Agora cada link
  devolvido é aberto (título real, texto, preço) e avaliado na hora; a busca
  só para quando uma página realmente se qualifica, senão segue para o
  próximo buscador (Google, Mojeek, Yahoo, ...).
- O log mostra cada página aberta: título, nota e motivo (ou o erro).

# v58 — jogo com outro nome no itch.io

- Quando o título do itch.io é diferente do RA (ex. "Saint Seiya - El regreso
  del Fénix" = "Knights of the Zodiac: The Phoenix Returns"), o app aceita a
  página como candidata se:
  - o texto da página cita o título do RA (ou a parte antes do ":"), ou
  - ela está entre os 3 primeiros resultados da busca "título + desenvolvedor".
  Console citado e desenvolvedor parecido somam pontos. O hash continua
  decidindo: página errada não instala nada.
- Desenvolvedor parecido: início igual de 6+ letras conta
  ("Zeichi Gameplay Short" ~ zeichigames.itch.io).
- Jogos "not on itch" são procurados de novo.

# v57 — busca na web: título + desenvolvedor, páginas lidas antes de julgar

- A busca na web não para mais no primeiro buscador que devolve qualquer
  link do itch: só para quando um resultado bate com o título ou o
  desenvolvedor. Buscador que bloqueia (202/403/429) é pulado.
- Ordem: Bing, Google, Mojeek, Yahoo, Startpage, DuckDuckGo, Brave.
- Consultas: "título desenvolvedor itch.io", depois "título itch.io", depois
  os nomes alternativos.
- Links achados na web têm a página aberta (título real, preço) ANTES de
  serem avaliados — o endereço do itch muitas vezes não é o título.
- Página do desenvolvedor também pela primeira palavra do nome
  ("Zeichi Gameplay Short" → zeichi.itch.io).

# v56 — busca na web mais forte + diagnóstico

- Busca na web como uma pessoa faria ("<título> itch.io", sem "site:"),
  tentando vários buscadores em ordem até um devolver links do itch:
  DuckDuckGo, DuckDuckGo Lite, Brave, Bing, Startpage, Google. Links
  embrulhados pelo Bing e pelo Google são decodificados.
- O log mostra o que cada buscador respondeu ("web search brave: 3 itch.io
  link(s)" ou o erro, ex. HTTP 403 quando bloqueia).
- --hub-find <ID>: roda todas as buscas para um jogo e mostra desenvolvedores,
  nomes alternativos e cada resultado com a pontuação.
- Jogos "not on itch" são procurados de novo.

# v55 — PlayStation/PSP verificados, nomes alternativos, instalar sem verificar

- Hash do RetroAchievements para discos: PlayStation (lê SYSTEM.CNF e o
  executável dentro do .bin/.cue ou .iso) e PSP (.iso: PARAM.SFO + EBOOT.BIN).
  O .cue é instalado junto com todas as faixas. CHD/PBP/CSO não dá para ler.
- Nomes alternativos na busca: títulos dos arquivos de hash do RA
  ("Casanova (World).md" para "Mega Casanova") e o título sem palavras como
  Mega/Super/DX. Jogos "not on itch" são procurados de novo.
- Instalar sem verificar: quando o hash não bate ou não dá para calcular, a
  página do jogo avisa; apertar A de novo instala o melhor resultado do itch
  mesmo assim (regra de pagos continua). Marcado como "unverified".
  Linha de comando: --hub-sync --only ID --unverified

# v54 — filtros do RetroAchievements; busca que não desiste

- Filtro (SELECT aplica):
  - Search: título, autor ou sistema.
  - System: consoles presentes no hub.
  - Order: By system, A-Z, Z-A, Most/Fewest achievements, Free first, Paid first.
  - Show: All, Installed, Not installed, Free, Paid, Bought on itch.io,
    Can be verified, Not found on itch.io.
  - O filtro 18+ foi removido.
- "not on itch" só depois de procurar em tudo: endereço direto
  (https://<dev>.itch.io/<título>), página do desenvolvedor, busca pela API
  do itch (com a sua key), mais buscas no itch.io e busca na web
  (site:itch.io "título"). A busca do itch.io esconde jogos marcados como
  adultos de quem não está logado — foi por isso que o Masturbrowse não
  aparecia. Os jogos que estavam "not on itch" são procurados de novo
  automaticamente pela varredura em segundo plano.

# v53 — preço na lista e na página; pago não comprado = parada imediata

- Lista: o selo mostra o preço ("Free", "$3.85", "$3.85 owned") no lugar de
  "found"/"verified" (instalados continuam com a marca verde).
- Página do jogo: preço no cabeçalho e na primeira linha da descrição
  (e se está ou não nas suas compras). "price: checking..." até a página
  do itch ser encontrada.
- Se o melhor resultado do itch.io for pago e você não comprou, o app para
  na hora: não abre outras páginas, não faz busca ampla, não baixa nada.

# v52 — busca do itch.io em segundo plano, com cache

- Ao abrir o app, uma tarefa em segundo plano procura cada jogo do hub no
  itch.io (busca rápida: título + desenvolvedor) e lê a página dele, salvando
  página, capa e descrição em ~/.local/share/leaf-itchio/ra-hub-3036.json.
  Um jogo por vez, com pausa entre eles; nada é baixado.
- O jogo onde o cursor para passa na frente da fila.
- O cabeçalho mostra "itch.io data X/Y" enquanto a varredura roda. Depois de
  tudo em cache, nada é buscado de novo ao abrir o app.
- Atualizações só quando você pede (Settings):
  - "Refresh RetroAchievements hub": jogos novos no hub e hashes; os novos
    entram na varredura automaticamente.
  - "Re-scan itch.io (all games)": refaz a busca de todos os jogos não
    instalados (páginas fixadas com --hub-set-itch e instalados são mantidos).

# v51 — página do jogo mais rápida, capas, descrição do itch na lista

- Busca em duas etapas: ao abrir/navegar só UMA busca "título + desenvolvedor"
  (e "título" sozinho se não achar nada). A busca ampla (página do
  desenvolvedor, título + console...) só roda se o arquivo baixado não bater
  o hash.
- "itch.io", "homebrew" etc. não são mais usados como nome de desenvolvedor.
- Capas: pedidos de imagem vão com User-Agent de navegador (o CDN do RA
  respondia 403). Assim que a página do itch é encontrada, a capa do itch
  substitui o ícone do RA.
- Lista principal: imagem maior (52% do painel, como no Leaf) e, embaixo, a
  descrição do itch.io no lugar das informações repetidas.

# v50 — pasta itchio, pagos só se comprados, busca pelo desenvolvedor

- ROMs instaladas vão para `/roms/<sistema>/itchio/` (a pasta do sistema vem
  do es_systems.cfg; a extensão é escolhida entre as que o EmulationStation
  aceita para aquele sistema). A entrada no gamelist.xml fica no do sistema,
  como `./itchio/Nome.ext`. Instalações da v49 são movidas automaticamente.
- Jogo pago: só é baixado se estiver nas suas compras do itch.io (faça login
  com a API key do itch em Settings, ou `--login`). Página com preço é pulada
  sem nem ser aberta. Status na lista: "paid (not owned)".
- Busca: nomes de desenvolvedor vêm do RA (Developer/Publisher e nomes entre
  parênteses nos arquivos de hash, ex. "(Nova32)"). Primeiro a página do
  desenvolvedor (https://<dev>.itch.io), depois busca "título + dev", título,
  "título + console". Resultados que citam o console ganham prioridade;
  jogos de navegador/PC que não citam o console perdem.
- Não existe mais "instalar tudo" na tela: você escolhe o jogo e aperta A.
  Rolar a lista não faz buscas no itch.io.

# v49 — catálogo = RetroAchievements Hub 3036, instalação só com hash verificado

A lista do app agora é exatamente os jogos do Hub 3036 do RetroAchievements
(`internal-api/hub/<id>/games`, paginado). O itch.io é só a fonte dos
arquivos: um arquivo só é instalado quando o hash RetroAchievements dele
(calculado como o rcheevos faz — NES sem o cabeçalho iNES, SNES sem cabeçalho
de copiadora, 7800/Lynx sem cabeçalho, N64 normalizado, NDS pelo método
próprio) bate com um hash do jogo.

Código novo: `internal/rahub/` (API do RA, hashes, busca no itch, extração,
estado retomável) e `cmd/poc/hub.go` (integração com a UI e a linha de comando).

## Configuração

    ./bin/leaf-mlp1-poc-arm64 --ra-login nicefrog SUA_CHAVE_WEB_API
    # ou: export RA_KEY=...   (RA_USER, RA_HUB_ID também funcionam)

A chave fica em ~/.local/share/leaf-itchio/config.json (permissão 0600) e
nunca é impressa sem máscara.

## Linha de comando (via SSH)

    --hub-sync                 processa o que falta (retoma de onde parou)
    --hub-sync --retry         tenta de novo "não achados" / "sem match"
    --hub-sync --only 26007    um jogo só
    --hub-sync --limit 20      no máximo 20 jogos nesta rodada
    update                     relê o hub e os hashes, depois sincroniza
    --hub-refresh              só relê o hub e os hashes
    --hub-status               relatório por sistema
    --hub-set-itch ID URL      fixa a página do itch.io de um jogo

Estado: ~/.local/share/leaf-itchio/ra-hub-3036.json. Downloads temporários
ficam em .../work (apagado a cada execução), nunca em /roms.

## Na tela

- Lista: console + status (verified / found / not on itch / no hash match /
  no RA hash / disc: can't verify / retry). Capa = ícone do RA até achar a
  página do itch.
- A no detalhe: busca → download → extração → hash → instala só se bater.
- START > Settings: chave do RA, "Refresh RetroAchievements hub",
  "Install all (verified only)" / "Stop installing".
- A pasta de destino vem do es_systems.cfg; se o dArkOS não tem o sistema,
  nada é baixado.

## Limitações

- Sistemas de disco (PSX, Sega CD, PCE CD, Saturn, DC, PSP, 3DO): hash não
  implementado → não instala (regra estrita).
- RAR não suportado. Jogos pagos: o itch não mostra arquivos sem compra.
- A busca lê o HTML de itch.io/search; se o site mudar, o jogo aparece como
  "not on itch" (nunca instala nada errado). Use --hub-set-itch.
- Não testado ainda contra o RA/itch.io reais nem no aparelho.

---

# Leaf-Itchio-Pak → dArkOS port (PoC)

Proof of concept: browses itch.io's public catalog and shows a game detail
screen, running on plain SDL2 instead of Leaf's closed-source "Catastrophe"
UI library. Built, compiled, and click-tested (list → detail → back) inside
a sandboxed dev container using a virtual display — **not yet tested on a
real Miniloong / dArkOS device.**

## What's real (copied verbatim from Leaf-Itchio-Pak v0.1.0, unmodified)

- `internal/appui/*.go` — all 9 screen models (main list, detail, filter,
  download, destination, manage, rename, settings, about/refresh)
- `internal/itchio` — the real itch.io client (public RSS browsing, auth,
  game detail scraping, download, checksum verification)
- `internal/inventory`, `internal/roms`, `internal/media`, `internal/settings`,
  `internal/power`, `internal/logger`, `internal/text` — supporting logic
- All of the above cross-compile clean for `GOOS=linux GOARCH=arm64` (checked
  in this environment, see below) with zero source changes.

## What's new

- `internal/sdlui` — replaces `internal/catui` + `cat_bridge.c`. Same role
  (button-input translation + `Draw()` per screen), implemented with plain
  SDL2/SDL2_ttf calls instead of Catastrophe. Only 2 of 9 screens are
  implemented (`DrawMainList`, `DrawDetail`) — the other 7 follow the exact
  same pattern against their existing `appui` model, see "What's left" below.
- Real `SDL_GameController` input (with keyboard as a PC-testing fallback).
  L2/R2 read the analog trigger axes (`SDL_CONTROLLER_AXIS_TRIGGERLEFT/RIGHT`)
  rather than being simulated as buttons.

## What's stubbed / not wired

- `internal/leaf` (the "Jawaka" daemon client — suspend protection, shared
  library sync between Leaf apps) is vendored but **not called from
  `main.go`**. dArkOS has no equivalent daemon; calling it would just fail to
  connect. Leaving it out means no suspend protection during downloads and no
  shared-library integration with other apps — degraded but functional.
- `DetailIntentDownload` only logs `"would start download for: <title>"`.
  Wiring a real download means picking a destination path (SD card layout is
  dArkOS-specific) and calling the already-vendored
  `itchio.Client.ParseDownloadPage` + `Client.DownloadURL` — both work today,
  just need a real path instead of a guess.
- `openDetail()` doesn't call `itchio.Client.FetchGameDetail()` for the real
  description/screenshots — it shows a placeholder string to keep the PoC's
  click-through fast. That's a one-line call away.

## Building on the actual dArkOS device

This sandbox's network egress is locked to a short allowlist (no
`golang.org`, no `ports.ubuntu.com`, no Go module proxy), so `go.mod` here
has `replace` directives pointing dependencies at their GitHub mirrors — a
workaround for *this container only*. On the real device, with normal
internet:

```sh
sudo apt update
sudo apt install -y golang-go libsdl2-dev libsdl2-ttf-dev

cd leaf-mlp1-poc
# Delete the `replace (...)` block at the bottom of go.mod first —
# it's a sandbox-only workaround, not needed with real internet.
go mod tidy
go build -o bin/leaf-mlp1-poc-arm64 ./cmd/poc
```

Confirm `libsdl2-dev`/`libsdl2-ttf-dev` are actually available in dArkOS's
apt sources before this — it's Debian-based so they almost certainly are
(EmulationStation itself depends on SDL2), but that's an assumption, not
something this sandbox could verify.

## What I could verify here vs. what still needs the real hardware

| Checked in this sandbox | Still needs your Miniloong |
|---|---|
| Compiles clean, `go vet` clean | Compiles on dArkOS's actual toolchain/libc |
| Runs under Xvfb, renders list + detail correctly (screenshots below) | Renders correctly on the real screen resolution/DPI |
| Keyboard input → correct `appui` intents | Real gamepad button IDs (`SDL_GameControllerOpen` should auto-map most pads via SDL's built-in DB, but worth confirming on first run) |
| `GOARCH=arm64` cross-compile of all non-cgo packages | Actually linking against dArkOS's real SDL2 build |
| itch.io client code compiles and matches the real API surface | An actual network call to itch.io (blocked from this sandbox) |

## Screenshots (from this sandbox, Xvfb + demo data)

`list.png` — main catalog list, `detail.png` — after pressing A on a game.

## Next screens to port (same recipe each time)

For each remaining `internal/appui/*.go` model: read its `Draw`-relevant
fields, write one `Screen.DrawX(model *appui.XModel)` method in `sdlui.go`
following `DrawMainList`/`DrawDetail` as a template, wire its `Handle()`
intents into `main.go`'s screen-mode switch. Filter and Settings are
probably next, since they're reachable from the list screen you already have.
